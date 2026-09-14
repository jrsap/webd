package webd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func freePort(t *testing.T) uint16 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer l.Close()
	return uint16(l.Addr().(*net.TCPAddr).Port)
}

func testLog() *log.Logger { return log.New(io.Discard, "", 0) }

func fetch(urlStr string, skipTLS bool) (int, string, error) {
	c := &http.Client{Timeout: 5 * time.Second}
	if skipTLS {
		c.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	resp, err := c.Get(urlStr)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), nil
}

func TestServeStaticAndProxy(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("static hi"), 0o644)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("backend " + r.URL.Path))
	}))
	defer backend.Close()

	p := freePort(t)
	cfg := Config{
		Ports: map[uint16]*PortConfig{p: {Routes: map[string]*Target{
			"/":    {Path: dir},
			"/api": {URL: backend.URL},
		}}},
	}
	srv, err := New(cfg, Options{Log: testLog()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Shutdown(context.Background())

	if code, body, err := fetch(fmt.Sprintf("http://127.0.0.1:%d/hello.txt", p), false); err != nil || code != 200 || body != "static hi" {
		t.Fatalf("static: %d %q %v", code, body, err)
	}
	if code, body, err := fetch(fmt.Sprintf("http://127.0.0.1:%d/api/x", p), false); err != nil || code != 200 || body != "backend /api/x" {
		t.Fatalf("proxy: %d %q %v", code, body, err)
	}
	if code, _, err := fetch(fmt.Sprintf("http://127.0.0.1:%d/missing.txt", p), false); err != nil || code != 404 {
		t.Fatalf("missing: %d %v", code, err)
	}
}

func TestServeTLS(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("secure"), 0o644)
	cert, key := selfSignedCert(t)

	p := freePort(t)
	cfg := Config{
		Ports: map[uint16]*PortConfig{p: {TLS: &TLSConfig{Certificate: cert, PrivateKey: key}, Routes: map[string]*Target{
			"/": {Path: dir},
		}}},
	}
	srv, err := New(cfg, Options{Log: testLog()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Shutdown(context.Background())

	if code, body, err := fetch(fmt.Sprintf("https://127.0.0.1:%d/", p), true); err != nil || code != 200 || body != "secure" {
		t.Fatalf("tls: %d %q %v", code, body, err)
	}
}

func selfSignedCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDer, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	writePEM(t, certPath, "CERTIFICATE", der)
	writePEM(t, keyPath, "PRIVATE KEY", keyDer)
	return certPath, keyPath
}

func writePEM(t *testing.T, path, typ string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pem.Encode(f, &pem.Block{Type: typ, Bytes: b})
}

type e2eRuntime struct {
	mu    sync.Mutex
	pulls int
}

func (rt *e2eRuntime) pulled() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.pulls
}

func (rt *e2eRuntime) Pull(context.Context, string) error {
	rt.mu.Lock()
	rt.pulls++
	rt.mu.Unlock()
	return nil
}

func (rt *e2eRuntime) Start(_ context.Context, _, _ string, publish map[uint16]uint16) (Process, error) {
	var servers []*http.Server
	var listeners []net.Listener
	for _, h := range publish {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", h))
		if err != nil {
			return nil, err
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("container " + r.URL.Path))
		})}
		servers = append(servers, srv)
		listeners = append(listeners, ln)
		go srv.Serve(ln)
	}
	return &e2eProcess{servers: servers, listeners: listeners, done: make(chan struct{})}, nil
}

type e2eProcess struct {
	servers   []*http.Server
	listeners []net.Listener
	done      chan struct{}
	closeOnce sync.Once
}

func (p *e2eProcess) Wait() error { <-p.done; return nil }

func (p *e2eProcess) Stop() error {
	p.closeOnce.Do(func() {
		for _, s := range p.servers {
			s.Close()
		}
		for _, l := range p.listeners {
			l.Close()
		}
		close(p.done)
	})
	return nil
}

func TestServeContainerRoute(t *testing.T) {
	rt := &e2eRuntime{}
	p := freePort(t)
	cfg := Config{
		Ports: map[uint16]*PortConfig{p: {Routes: map[string]*Target{
			"/app": {Container: "c", Port: 3000},
			"/m":   {Container: "c", Port: 9090},
		}}},
		Containers: map[string]*ContainerConfig{"c": {Image: "img"}},
	}
	srv, err := New(cfg, Options{Runtime: rt, Log: testLog()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Shutdown(context.Background())

	if code, body, err := fetch(fmt.Sprintf("http://127.0.0.1:%d/app/deep", p), false); err != nil || code != 200 || body != "container /app/deep" {
		t.Fatalf("container route: %d %q %v", code, body, err)
	}
	if code, body, err := fetch(fmt.Sprintf("http://127.0.0.1:%d/m", p), false); err != nil || code != 200 || body != "container /m" {
		t.Fatalf("second port route: %d %q %v", code, body, err)
	}
}

func TestBackgroundPullAtStart(t *testing.T) {
	rt := &e2eRuntime{}
	p := freePort(t)
	cfg := Config{
		Ports:      map[uint16]*PortConfig{p: {Routes: map[string]*Target{"/": {Container: "c", Port: 3000}}}},
		Containers: map[string]*ContainerConfig{"c": {Image: "img", Autostart: false}},
	}
	srv, err := New(cfg, Options{Runtime: rt, Log: testLog()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for rt.pulled() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if rt.pulled() == 0 {
		t.Fatal("images must be pulled in the background at startup")
	}
	srv.Shutdown(context.Background())
}
