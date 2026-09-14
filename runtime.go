package webd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
)

// Process is a running container: Wait blocks until it exits, Stop terminates it.
type Process interface {
	Wait() error
	Stop() error
}

// Runtime runs container images. Implemented by Podman; tests use fakes.
type Runtime interface {
	Pull(ctx context.Context, image string) error
	// Start runs image publishing each containerPort on its assigned host port
	// at 127.0.0.1, under the given container name.
	Start(ctx context.Context, name, image string, publish map[uint16]uint16) (Process, error)
}

type Podman struct {
	Bin string
	// Runner launches the podman binary; a test seam, exec-based by default.
	Runner func(ctx context.Context, name string, args ...string) (Process, error)
}

func (p *Podman) bin() string {
	if p.Bin == "" {
		return "podman"
	}
	return p.Bin
}

func (p *Podman) runner() func(context.Context, string, ...string) (Process, error) {
	if p.Runner != nil {
		return p.Runner
	}
	return execProcess
}

func (p *Podman) Pull(ctx context.Context, image string) error {
	proc, err := p.runner()(ctx, p.bin(), "pull", "--policy", "missing", image)
	if err != nil {
		return fmt.Errorf("podman pull: %w", err)
	}
	return proc.Wait()
}

func (p *Podman) Start(ctx context.Context, name, image string, publish map[uint16]uint16) (Process, error) {
	args := []string{"run", "--rm", "--name", name}
	ports := make([]uint16, 0, len(publish))
	for c := range publish {
		ports = append(ports, c)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	for _, c := range ports {
		args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d", publish[c], c))
	}
	args = append(args, image)
	proc, err := p.runner()(ctx, p.bin(), args...)
	if err != nil {
		return nil, fmt.Errorf("podman run %s: %w", image, err)
	}
	return &podmanProc{rt: p, name: name, proc: proc}, nil
}

type podmanProc struct {
	rt   *Podman
	name string
	proc Process
}

func (pp *podmanProc) Wait() error { return pp.proc.Wait() }

// Stop signals the container to die; completion is observed through Wait,
// so it does not block on the run process itself.
func (pp *podmanProc) Stop() error {
	kill, err := pp.rt.runner()(context.Background(), pp.rt.bin(), "kill", pp.name)
	if err != nil {
		return fmt.Errorf("podman kill %s: %w", pp.name, err)
	}
	if err := kill.Wait(); err != nil {
		return fmt.Errorf("podman kill %s: %w", pp.name, err)
	}
	return nil
}

func execProcess(ctx context.Context, name string, args ...string) (Process, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr // podman and container errors surface in webd's log
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProc{cmd}, nil
}

type execProc struct{ cmd *exec.Cmd }

func (e *execProc) Wait() error { return e.cmd.Wait() }

func (e *execProc) Stop() error {
	e.cmd.Process.Kill()
	return e.cmd.Wait()
}
