package webd

import (
	"context"
	"strings"
	"testing"
)

func fakeRunner(got *[][]string) func(context.Context, string, ...string) (Process, error) {
	return func(_ context.Context, name string, args ...string) (Process, error) {
		*got = append(*got, append([]string{name}, args...))
		return nopProc{}, nil
	}
}

type nopProc struct{}

func (nopProc) Wait() error { return nil }
func (nopProc) Stop() error { return nil }

func TestPodmanPullArgs(t *testing.T) {
	var got [][]string
	rt := &Podman{Bin: "podman", Runner: fakeRunner(&got)}
	if err := rt.Pull(context.Background(), "img:1"); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	want := "podman pull --policy missing img:1"
	if got := strings.Join(got[0], " "); got != want {
		t.Fatalf("argv: %q want %q", got, want)
	}
}

func TestPodmanStartArgs(t *testing.T) {
	var got [][]string
	rt := &Podman{Bin: "podman", Runner: fakeRunner(&got)}
	p, err := rt.Start(context.Background(), "webd-tafels", "quay.io/x/tafels:latest", map[uint16]uint16{3000: 20001, 9090: 20002})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if p == nil {
		t.Fatal("no process")
	}
	line := strings.Join(got[0], " ")
	for _, want := range []string{"podman run", "--rm", "--name webd-tafels", "-p 127.0.0.1:20001:3000", "-p 127.0.0.1:20002:9090", "quay.io/x/tafels:latest"} {
		if !strings.Contains(line, want) {
			t.Errorf("argv %q missing %q", line, want)
		}
	}
}

func TestPodmanStop(t *testing.T) {
	var got [][]string
	rt := &Podman{Bin: "podman", Runner: fakeRunner(&got)}
	p, err := rt.Start(context.Background(), "webd-x", "img", map[uint16]uint16{80: 20000})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	line := strings.Join(got[len(got)-1], " ")
	if !strings.Contains(line, "podman kill webd-x") {
		t.Fatalf("want kill, got %q", line)
	}
}
