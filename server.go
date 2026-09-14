package webd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
)

type Options struct {
	Runtime Runtime
	Log     *log.Logger
}

// Server serves a parsed config: one HTTP(S) listener per configured port,
// backed by static, proxy or container handlers.
type Server struct {
	log     *log.Logger
	entries []entry
	sups    []*supervisor
	wg      sync.WaitGroup
}

type entry struct {
	server   *http.Server
	listener net.Listener
	tls      *TLSConfig
}

func New(cfg Config, opts Options) (*Server, error) {
	s := &Server{log: opts.Log}
	if s.log == nil {
		s.log = log.New(os.Stderr, "webd: ", log.LstdFlags)
	}
	rt := opts.Runtime
	if rt == nil {
		rt = &Podman{}
	}

	publish := map[string]map[uint16]uint16{}
	for _, pc := range cfg.Ports {
		for _, t := range pc.Routes {
			if t.Container == "" {
				continue
			}
			if publish[t.Container] == nil {
				publish[t.Container] = map[uint16]uint16{}
			}
			publish[t.Container][t.Port] = 0
		}
	}
	total := 0
	for _, m := range publish {
		total += len(m)
	}
	hosts, err := freePorts(total)
	if err != nil {
		return nil, err
	}
	i := 0
	for _, name := range slices.Sorted(maps.Keys(publish)) {
		for _, c := range slices.Sorted(maps.Keys(publish[name])) {
			publish[name][c] = hosts[i]
			i++
		}
	}

	sups := map[string]*supervisor{}
	for _, name := range slices.Sorted(maps.Keys(cfg.Containers)) {
		sups[name] = newSupervisor(name, *cfg.Containers[name], rt, publish[name], s.log)
		s.sups = append(s.sups, sups[name])
	}

	for _, port := range slices.Sorted(maps.Keys(cfg.Ports)) {
		pc := cfg.Ports[port]
		handlers := map[string]http.Handler{}
		for path, t := range pc.Routes {
			switch {
			case t.Path != "":
				handlers[path] = staticHandler(t.Path)
			case t.URL != "":
				handlers[path] = proxyHandler(t.URL)
			default:
				handlers[path] = sups[t.Container].Handler(t.Port)
			}
		}
		srv := &http.Server{Handler: newRouter(handlers), ErrorLog: s.log}
		if pc.TLS != nil {
			cert, err := tls.LoadX509KeyPair(pc.TLS.Certificate, pc.TLS.PrivateKey)
			if err != nil {
				return nil, fmt.Errorf("port %d: tls: %w", port, err)
			}
			srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
		}
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			return nil, fmt.Errorf("port %d: %w", port, err)
		}
		s.entries = append(s.entries, entry{server: srv, listener: ln})
		s.log.Printf("listening on %s (tls: %v)", ln.Addr(), pc.TLS != nil)
	}
	return s, nil
}

// Start serves all listeners in the background.
func (s *Server) Start() error {
	for _, sup := range s.sups {
		sup.Warm()
	}
	for _, e := range s.entries {
		s.wg.Add(1)
		go func(e entry) {
			defer s.wg.Done()
			var err error
			if e.server.TLSConfig != nil {
				err = e.server.ServeTLS(e.listener, "", "")
			} else {
				err = e.server.Serve(e.listener)
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.log.Printf("serve %s: %v", e.listener.Addr(), err)
			}
		}(e)
	}
	return nil
}

// Shutdown stops accepting, waits for in-flight requests, and stops containers.
func (s *Server) Shutdown(ctx context.Context) error {
	var errs []error
	for _, e := range s.entries {
		if err := e.server.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	for _, sup := range s.sups {
		sup.Close()
	}
	s.wg.Wait()
	return errors.Join(errs...)
}

func freePorts(n int) ([]uint16, error) {
	ports := []uint16{}
	seen := map[uint16]bool{}
	listeners := []net.Listener{}
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
	}()
	for len(ports) < n {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, l)
		if p := uint16(l.Addr().(*net.TCPAddr).Port); !seen[p] {
			seen[p] = true
			ports = append(ports, p)
		}
	}
	return ports, nil
}
