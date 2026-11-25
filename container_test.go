package webd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
)

type fakeProc struct {
	mu     sync.Mutex
	die    chan struct{} // close to simulate the container exiting on its own
	stop   chan struct{}
	stopOK sync.Once
}

func newProc() *fakeProc { return &fakeProc{die: make(chan struct{}), stop: make(chan struct{})} }

var errCrashed = errors.New("container crashed")

func (p *fakeProc) Wait() error {
	select {
	case <-p.die:
		return errCrashed
	case <-p.stop:
		return nil
	}
}

func (p *fakeProc) Stop() error {
	p.stopOK.Do(func() { close(p.stop) })
	return nil
}

func (p *fakeProc) crash() { p.stopOK.Do(func() { close(p.die) }) }

func (p *fakeProc) stopped() bool {
	select {
	case <-p.stop:
		return true
	default:
		return false
	}
}

type fakeRT struct {
	mu     sync.Mutex
	procs  []*fakeProc
	gate   chan struct{} // if set, Start blocks until closed
	fail   error
	starts int
	pulls  int
	vols   []Volume // volumes of the last Start call
}

func (rt *fakeRT) Pull(context.Context, string) error {
	rt.mu.Lock()
	rt.pulls++
	rt.mu.Unlock()
	return nil
}

func (rt *fakeRT) Start(_ context.Context, _, _ string, _ map[uint16]uint16, vols []Volume) (Process, error) {
	rt.mu.Lock()
	rt.starts++
	rt.vols = append([]Volume(nil), vols...)
	gate, fail := rt.gate, rt.fail
	rt.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if fail != nil {
		return nil, fail
	}
	p := newProc()
	rt.mu.Lock()
	rt.procs = append(rt.procs, p)
	rt.mu.Unlock()
	return p, nil
}

func (rt *fakeRT) started() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.starts
}

func (rt *fakeRT) last() *fakeProc {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.procs) == 0 {
		return nil
	}
	return rt.procs[len(rt.procs)-1]
}

func (rt *fakeRT) lastVols() []Volume {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.vols
}

func testPorts(t *testing.T) map[uint16]uint16 {
	hello := func(tag string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(tag + r.URL.Path)) }
	}
	a := httptest.NewServer(hello("A"))
	b := httptest.NewServer(hello("B"))
	t.Cleanup(func() { a.Close(); b.Close() })
	return map[uint16]uint16{3000: portOf(t, a), 9090: portOf(t, b)}
}

func portOf(t *testing.T, s *httptest.Server) uint16 {
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatalf("portOf: %v", err)
	}
	_, p, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("portOf: %v", err)
	}
	n, err := strconv.ParseUint(p, 10, 16)
	if err != nil {
		t.Fatalf("portOf: %v", err)
	}
	return uint16(n)
}

func newTestSup(rt Runtime, cfg ContainerConfig, pub map[uint16]uint16) *supervisor {
	return newSupervisor("x", cfg, rt, pub, log.New(io.Discard, "", 0))
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://webd"+path, nil))
	return rec
}

func waitStopped(t *testing.T, sup *supervisor) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		sup.mu.Lock()
		stopped := sup.proc == nil && !sup.starting
		sup.mu.Unlock()
		if stopped {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("supervisor never noticed container exit")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestContainerLazyStartAndIdle(t *testing.T) {
	rt := &fakeRT{}
	pub := testPorts(t)
	sup := newTestSup(rt, ContainerConfig{IdleTimeout: 30 * time.Millisecond}, pub)
	defer sup.Close()

	if rt.started() != 0 {
		t.Fatal("must not start until first request")
	}
	if rec := get(sup.Handler(3000), "/foo"); rec.Code != 200 || rec.Body.String() != "A/foo" {
		t.Fatalf("got %d %q", rec.Code, rec.Body)
	}
	if rec := get(sup.Handler(9090), "/m"); rec.Body.String() != "B/m" {
		t.Fatalf("port routing broken: %d %q", rec.Code, rec.Body)
	}
	if rt.started() != 1 {
		t.Fatalf("want one start, got %d", rt.started())
	}
	if rt.last().stopped() {
		t.Fatal("stopped too early")
	}
	time.Sleep(100 * time.Millisecond)
	if !rt.last().stopped() {
		t.Fatal("not stopped after idle timeout")
	}
	if rec := get(sup.Handler(3000), "/x"); rec.Body.String() != "A/x" {
		t.Fatalf("%q", rec.Body)
	}
	if rt.started() != 2 {
		t.Fatalf("want restart, starts=%d", rt.started())
	}
}

func TestContainerTrafficResetsIdle(t *testing.T) {
	rt := &fakeRT{}
	pub := testPorts(t)
	sup := newTestSup(rt, ContainerConfig{IdleTimeout: 60 * time.Millisecond}, pub)
	defer sup.Close()

	get(sup.Handler(3000), "/")
	time.Sleep(30 * time.Millisecond)
	get(sup.Handler(9090), "/") // traffic on the other route resets the shared timer
	time.Sleep(45 * time.Millisecond)
	if rt.last().stopped() {
		t.Fatal("container stopped despite traffic on another route")
	}
	time.Sleep(60 * time.Millisecond)
	if !rt.last().stopped() {
		t.Fatal("container not stopped after quiet period")
	}
}

func TestContainerNoIdleTimeout(t *testing.T) {
	rt := &fakeRT{}
	pub := testPorts(t)
	sup := newTestSup(rt, ContainerConfig{}, pub)
	defer sup.Close()
	get(sup.Handler(3000), "/")
	time.Sleep(50 * time.Millisecond)
	if rt.last().stopped() {
		t.Fatal("IdleTimeout 0 must mean never stop")
	}
}

func TestContainerConcurrentFirstRequests(t *testing.T) {
	gate := make(chan struct{})
	rt := &fakeRT{gate: gate}
	pub := testPorts(t)
	sup := newTestSup(rt, ContainerConfig{}, pub)
	defer sup.Close()

	h := sup.Handler(3000)
	var wg sync.WaitGroup
	errs := make(chan string, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rec := get(h, "/c"); rec.Code != 200 || rec.Body.String() != "A/c" {
				errs <- fmt.Sprintf("%d %q", rec.Code, rec.Body)
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent request failed: %s", e)
	}
	if rt.started() != 1 {
		t.Fatalf("want exactly one start, got %d", rt.started())
	}
}

func TestContainerAutostart(t *testing.T) {
	rt := &fakeRT{}
	pub := testPorts(t)
	sup := newTestSup(rt, ContainerConfig{Autostart: true}, pub)
	defer sup.Close()

	deadline := time.Now().Add(time.Second)
	for rt.started() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if rt.started() != 1 {
		t.Fatal("autostart container did not start")
	}
}

func TestContainerCrashRestarts(t *testing.T) {
	rt := &fakeRT{}
	pub := testPorts(t)
	sup := newTestSup(rt, ContainerConfig{}, pub)
	defer sup.Close()

	get(sup.Handler(3000), "/")
	rt.last().crash()
	waitStopped(t, sup)
	if rec := get(sup.Handler(3000), "/again"); rec.Code != 200 || rec.Body.String() != "A/again" {
		t.Fatalf("after crash: %d %q", rec.Code, rec.Body)
	}
	if rt.started() != 2 {
		t.Fatalf("want restart after crash, starts=%d", rt.started())
	}
}

func TestContainerStartFailure(t *testing.T) {
	rt := &fakeRT{fail: fmt.Errorf("boom")}
	pub := testPorts(t)
	sup := newTestSup(rt, ContainerConfig{}, pub)
	defer sup.Close()

	if rec := get(sup.Handler(3000), "/"); rec.Code != 502 {
		t.Fatalf("want 502, got %d", rec.Code)
	}
	rt.mu.Lock()
	rt.fail = nil
	rt.mu.Unlock()
	if rec := get(sup.Handler(3000), "/ok"); rec.Code != 200 {
		t.Fatalf("want retry after fix to succeed, got %d", rec.Code)
	}
}

func TestContainerInFlightBlocksIdle(t *testing.T) {
	received := make(chan struct{})
	release := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(received)
		<-release
		w.Write([]byte("slow"))
	}))
	defer backend.Close()

	rt := &fakeRT{}
	pub := map[uint16]uint16{3000: portOf(t, backend)}
	sup := newTestSup(rt, ContainerConfig{IdleTimeout: 20 * time.Millisecond}, pub)
	defer sup.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if rec := get(sup.Handler(3000), "/"); rec.Body.String() != "slow" {
			t.Errorf("in-flight request: %d %q", rec.Code, rec.Body)
		}
	}()
	<-received
	time.Sleep(80 * time.Millisecond)
	if rt.last().stopped() {
		t.Fatal("must not idle-stop while a request is in flight")
	}
	close(release)
	<-done
}

func TestContainerWaitsForReadiness(t *testing.T) {
	oldTimeout := startupTimeout
	startupTimeout = 3 * time.Second
	t.Cleanup(func() { startupTimeout = oldTimeout })

	rt := &fakeRT{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := uint16(ln.Addr().(*net.TCPAddr).Port)
	ln.Close()

	started := make(chan *http.Server, 1)
	go func() {
		time.Sleep(150 * time.Millisecond)
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			return
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("late"))
		})}
		started <- srv
		srv.Serve(l)
	}()
	sup := newTestSup(rt, ContainerConfig{}, map[uint16]uint16{3000: p})
	defer func() {
		select {
		case s := <-started:
			s.Close()
		default:
		}
		sup.Close()
	}()

	if rec := get(sup.Handler(3000), "/"); rec.Code != 200 || rec.Body.String() != "late" {
		t.Fatalf("request before container was ready: %d %q", rec.Code, rec.Body)
	}
}

func TestContainerPassesVolumesToRuntime(t *testing.T) {
	rt := &fakeRT{}
	pub := testPorts(t)
	vols := []Volume{{Host: filepath.Join(t.TempDir(), "data"), Container: "/data", ReadOnly: true}}
	sup := newTestSup(rt, ContainerConfig{Volumes: vols}, pub)
	defer sup.Close()

	if rec := get(sup.Handler(3000), "/"); rec.Code != 200 {
		t.Fatalf("got %d %q", rec.Code, rec.Body)
	}
	if got := rt.lastVols(); !reflect.DeepEqual(got, vols) {
		t.Fatalf("runtime got volumes %+v, want %+v", got, vols)
	}
}

func TestContainerCreatesMissingVolumeDirs(t *testing.T) {
	rt := &fakeRT{}
	pub := testPorts(t)
	dir := filepath.Join(t.TempDir(), "data", "nested")
	vols := []Volume{{Host: dir, Container: "/data"}}
	sup := newTestSup(rt, ContainerConfig{Volumes: vols}, pub)
	defer sup.Close()

	if rec := get(sup.Handler(3000), "/"); rec.Code != 200 {
		t.Fatalf("got %d %q", rec.Code, rec.Body)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("host dir %s was not created: %v", dir, err)
	}
}

func TestContainerVolumeDirFailure(t *testing.T) {
	rt := &fakeRT{}
	pub := testPorts(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a path below a regular file can never be created as a directory
	vols := []Volume{{Host: filepath.Join(blocker, "data"), Container: "/data"}}
	sup := newTestSup(rt, ContainerConfig{Volumes: vols}, pub)
	defer sup.Close()

	rec := get(sup.Handler(3000), "/")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d %q", rec.Code, rec.Body)
	}
	if rt.started() != 0 {
		t.Fatal("runtime must not start when a volume host dir cannot be created")
	}
}

func TestContainerCloseStops(t *testing.T) {
	rt := &fakeRT{}
	pub := testPorts(t)
	sup := newTestSup(rt, ContainerConfig{}, pub)
	get(sup.Handler(3000), "/")
	sup.Close()
	if !rt.last().stopped() {
		t.Fatal("Close must stop the container")
	}
	if rec := get(sup.Handler(3000), "/"); rec.Code != 502 {
		t.Fatalf("after Close want 502, got %d", rec.Code)
	}
}

type crashOnceRT struct {
	fakeRT
	hostPort uint16
}

func (rt *crashOnceRT) Start(ctx context.Context, name, image string, publish map[uint16]uint16, _ []Volume) (Process, error) {
	rt.mu.Lock()
	first := rt.starts == 0
	rt.starts++
	rt.mu.Unlock()
	if first {
		p := newProc()
		go func() { time.Sleep(10 * time.Millisecond); p.crash() }()
		rt.mu.Lock()
		rt.procs = append(rt.procs, p)
		rt.mu.Unlock()
		return p, nil
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", rt.hostPort))
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("late2" + r.URL.Path))
	})}
	go srv.Serve(ln)
	return &e2eProcess{servers: []*http.Server{srv}, listeners: []net.Listener{ln}, done: make(chan struct{})}, nil
}

func TestContainerDeathDuringStartup(t *testing.T) {
	oldTimeout := startupTimeout
	startupTimeout = 2 * time.Second
	t.Cleanup(func() { startupTimeout = oldTimeout })

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host := uint16(closed.Addr().(*net.TCPAddr).Port)
	closed.Close()
	rt := &crashOnceRT{hostPort: host}

	sup := newTestSup(rt, ContainerConfig{}, map[uint16]uint16{3000: host})
	defer sup.Close()
	// first attempt crashes before binding host: readiness must fail the request
	if rec := get(sup.Handler(3000), "/"); rec.Code == 200 {
		t.Fatal("want failure when container dies during startup")
	}
	waitStopped(t, sup)
	if rec := get(sup.Handler(3000), "/ok"); rec.Code != 200 || rec.Body.String() != "late2/ok" {
		t.Fatalf("want restart to serve, got %d %q", rec.Code, rec.Body)
	}
}
