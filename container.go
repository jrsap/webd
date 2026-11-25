package webd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

// startupTimeout bounds how long a started container may take to accept
// connections on all its published ports.
var startupTimeout = 30 * time.Second

// supervisor owns one container's lifecycle: it is started on demand (or at
// startup when autostarted), shared by all routes that reference it across all
// listening ports, and stopped once no requests are in flight and no traffic
// arrived for IdleTimeout.
type supervisor struct {
	name    string
	cfg     ContainerConfig
	rt      Runtime
	log     *log.Logger
	publish map[uint16]uint16
	proxies map[uint16]*httputil.ReverseProxy

	mu       sync.Mutex
	wake     chan struct{} // closed and replaced on every state transition
	starting bool
	startErr error
	proc     Process
	refs     int
	idle     *time.Timer
	closed   bool
}

func newSupervisor(name string, cfg ContainerConfig, rt Runtime, publish map[uint16]uint16, l *log.Logger) *supervisor {
	s := &supervisor{
		name:    name,
		cfg:     cfg,
		rt:      rt,
		log:     l,
		wake:    make(chan struct{}),
		publish: publish,
		proxies: map[uint16]*httputil.ReverseProxy{},
	}
	for c, h := range publish {
		s.proxies[c] = httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", h)})
	}
	if cfg.Autostart {
		go func() {
			if err := s.acquire(context.Background()); err == nil {
				s.release()
			}
		}()
	}
	return s
}

// Warm prefetches the image in the background so the first request does not
// pay for the pull.
func (s *supervisor) Warm() {
	if s.cfg.Autostart {
		return
	}
	go func() {
		if err := s.rt.Pull(context.Background(), s.cfg.Image); err != nil {
			s.log.Printf("container %s: background pull: %v", s.name, err)
		}
	}()
}

// Handler routes requests for the given container port, starting the
// container if needed.
func (s *supervisor) Handler(port uint16) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.acquire(r.Context()); err != nil {
			http.Error(w, fmt.Sprintf("container %s unavailable: %v", s.name, err), http.StatusBadGateway)
			return
		}
		defer s.release()
		proxy, ok := s.proxies[port]
		if !ok {
			http.Error(w, fmt.Sprintf("container %s does not publish port %d", s.name, port), http.StatusInternalServerError)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

func (s *supervisor) acquire(ctx context.Context) error {
	s.mu.Lock()
	s.refs++
	s.stopIdleLocked()
	kicked := false
	for {
		if s.closed {
			s.releaseLocked()
			s.mu.Unlock()
			return errors.New("shutting down")
		}
		if s.starting {
			// wait for the in-progress attempt, including readiness
		} else if s.proc != nil {
			s.mu.Unlock()
			return nil
		} else if kicked && s.startErr != nil {
			err := s.startErr
			s.releaseLocked()
			s.mu.Unlock()
			return err
		} else {
			s.starting = true
			s.startErr = nil
			kicked = true
			go s.start()
		}
		w := s.wake
		s.mu.Unlock()
		select {
		case <-w:
		case <-ctx.Done():
			s.mu.Lock()
			s.releaseLocked()
			s.mu.Unlock()
			return ctx.Err()
		}
		s.mu.Lock()
	}
}

func (s *supervisor) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseLocked()
}

// start pulls and runs the container outside the lock, then records the result.
func (s *supervisor) start() {
	ctx := context.Background()
	err := s.rt.Pull(ctx, s.cfg.Image)
	var proc Process
	if err == nil {
		proc, err = s.rt.Start(ctx, "webd-"+s.name, s.cfg.Image, s.publish, s.cfg.Volumes)
	}
	s.mu.Lock()
	if err != nil {
		s.log.Printf("container %s: %v", s.name, err)
		s.startErr = err
		s.starting = false
		s.signalLocked()
		s.mu.Unlock()
		return
	}
	if s.closed {
		s.starting = false
		s.signalLocked()
		s.mu.Unlock()
		proc.Stop()
		return
	}
	s.proc = proc
	s.mu.Unlock()

	if err = s.waitReady(proc); err != nil {
		s.log.Printf("container %s: %v", s.name, err)
		proc.Stop()
		s.mu.Lock()
		if s.proc == proc {
			s.proc = nil
			s.startErr = err
			s.starting = false
			s.signalLocked()
		}
		s.mu.Unlock()
		return
	}

	s.mu.Lock()
	if s.proc == proc {
		s.starting = false
		s.signalLocked()
	}
	s.mu.Unlock()

	err = proc.Wait()

	s.mu.Lock()
	if s.proc == proc {
		if err != nil {
			s.log.Printf("container %s exited: %v", s.name, err)
			s.startErr = err
		}
		s.proc = nil
		s.starting = false
		s.signalLocked()
	}
	s.mu.Unlock()
}

// waitReady dials every published host port until all accept connections,
// giving up if the process dies or startupTimeout elapses.
func (s *supervisor) waitReady(proc Process) error {
	deadline := time.Now().Add(startupTimeout)
	for {
		s.mu.Lock()
		dead := s.proc != proc || s.closed
		s.mu.Unlock()
		if dead {
			return errors.New("died during startup")
		}
		if s.reachable() == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not ready within %v", startupTimeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (s *supervisor) reachable() error {
	for _, h := range s.publish {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", h), 500*time.Millisecond)
		if err != nil {
			return err
		}
		c.Close()
	}
	return nil
}

func (s *supervisor) releaseLocked() {
	s.refs--
	if s.refs == 0 && s.proc != nil && s.cfg.IdleTimeout > 0 {
		var t *time.Timer
		t = time.AfterFunc(s.cfg.IdleTimeout, func() {
			s.mu.Lock()
			if s.idle == t {
				s.idle = nil
			}
			if s.refs == 0 && !s.closed && s.proc != nil {
				proc := s.proc
				s.proc = nil
				s.signalLocked()
				s.mu.Unlock()
				if err := proc.Stop(); err != nil {
					s.log.Printf("container %s: idle stop: %v", s.name, err)
				}
				return
			}
			s.mu.Unlock()
		})
		s.idle = t
	}
}

func (s *supervisor) stopIdleLocked() {
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
}

func (s *supervisor) signalLocked() {
	close(s.wake)
	s.wake = make(chan struct{})
}

// Close stops the container and rejects further requests.
func (s *supervisor) Close() {
	s.mu.Lock()
	s.closed = true
	proc := s.proc
	s.proc = nil
	s.stopIdleLocked()
	s.signalLocked()
	s.mu.Unlock()
	if proc != nil {
		if err := proc.Stop(); err != nil {
			s.log.Printf("container %s: close: %v", s.name, err)
		}
	}
}
