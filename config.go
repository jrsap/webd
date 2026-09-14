package webd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Config struct {
	Ports      map[uint16]*PortConfig
	Containers map[string]*ContainerConfig
}

type PortConfig struct {
	TLS    *TLSConfig
	Routes map[string]*Target
}

type TLSConfig struct {
	Certificate string
	PrivateKey  string
}

// Target is a route's destination: exactly one of Path, URL or Container is set.
type Target struct {
	Path      string
	URL       string
	Container string
	Port      uint16
}

type ContainerConfig struct {
	Image       string
	IdleTimeout time.Duration
	Autostart   bool
}

var containerName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ParseConfig parses webd's JSON config. Keys of configuration objects are
// canonicalized (lower case, underscores removed) so privateKey and private_key
// are equivalent; route keys, container names and target strings are left
// untouched. It returns warnings for duplicate keys that canonicalize onto each
// other with equal values, and an error when they differ or when anything else
// is invalid.
func ParseConfig(data []byte) (Config, []string, error) {
	p := &parser{warnings: []string{}}
	cfg := p.parse(data)
	return cfg, p.warnings, p.err
}

type parser struct {
	warnings []string
	err      error
}

func (p *parser) parse(data []byte) Config {
	cfg := Config{Ports: map[uint16]*PortConfig{}, Containers: map[string]*ContainerConfig{}}
	top := p.object(data, "config")
	if raw, ok := top["containers"]; ok {
		delete(top, "containers")
		cfg.Containers = p.containers(raw)
	}
	for portKey, raw := range top {
		n, err := strconv.Atoi(portKey)
		if err != nil || n < 1 || n > 65535 {
			p.fail("config", "port %q must be a number between 1 and 65535, or \"containers\"", portKey)
			continue
		}
		cfg.Ports[uint16(n)] = p.portConfig(raw, portKey)
	}
	p.checkRefs(cfg)
	return cfg
}

func (p *parser) containers(raw json.RawMessage) map[string]*ContainerConfig {
	var m map[string]json.RawMessage
	p.decode("containers", raw, &m)
	out := map[string]*ContainerConfig{}
	for name, c := range m {
		if !containerName.MatchString(name) {
			p.fail("containers", "name %q must only contain letters, digits, - or _", name)
			continue
		}
		out[name] = p.containerConfig(c, "containers."+name)
	}
	return out
}

func (p *parser) containerConfig(raw json.RawMessage, ctx string) *ContainerConfig {
	obj := p.object(raw, ctx)
	p.known(ctx, obj, "image", "idletimeout", "autostart")
	c := &ContainerConfig{}
	if v, ok := obj["image"]; ok {
		c.Image = p.string(ctx+".image", v)
	}
	if c.Image == "" {
		p.fail(ctx, "\"image\" is required")
	}
	if v, ok := obj["idletimeout"]; ok {
		var d durationJSON
		p.decode(ctx+".idletimeout", v, &d)
		c.IdleTimeout = d.Duration
	}
	if v, ok := obj["autostart"]; ok {
		c.Autostart = p.bool(ctx+".autostart", v)
	}
	return c
}

// checkRefs reports route targets that reference undefined containers and
// containers that can never run (not referenced by any route, not autostarted).
func (p *parser) checkRefs(cfg Config) {
	used := map[string]bool{}
	for _, port := range cfg.Ports {
		for path, t := range port.Routes {
			if t.Container == "" {
				continue
			}
			if _, ok := cfg.Containers[t.Container]; !ok {
				p.fail("routes."+path, "container %q is not defined", t.Container)
				continue
			}
			used[t.Container] = true
		}
	}
	for name, c := range cfg.Containers {
		if !used[name] && !c.Autostart {
			p.warnings = append(p.warnings, fmt.Sprintf("containers.%s: not referenced by any route and autostart is false, it will never start", name))
		}
	}
}

func (p *parser) portConfig(raw json.RawMessage, ctx string) *PortConfig {
	obj := p.object(raw, ctx)
	p.known(ctx, obj, "tls", "routes")
	pc := &PortConfig{Routes: map[string]*Target{}}
	if tlsRaw, ok := obj["tls"]; ok {
		pc.TLS = p.tlsConfig(tlsRaw, ctx+".tls")
	}
	routesRaw, ok := obj["routes"]
	if !ok {
		p.fail(ctx, "missing \"routes\"")
		return pc
	}
	var routes map[string]json.RawMessage
	p.decode(ctx+".routes", routesRaw, &routes)
	if len(routes) == 0 {
		p.fail(ctx, "\"routes\" must not be empty")
	}
	for path, v := range routes {
		if !strings.HasPrefix(path, "/") {
			p.fail(ctx, "route key %q must start with /", path)
			continue
		}
		target := p.string(ctx+".routes."+path, v)
		t, err := parseTarget(target)
		if err != nil {
			p.fail(ctx+".routes."+path, "%v", err)
			continue
		}
		pc.Routes[path] = t
	}
	return pc
}

func (p *parser) tlsConfig(raw json.RawMessage, ctx string) *TLSConfig {
	obj := p.object(raw, ctx)
	p.known(ctx, obj, "certificate", "privatekey")
	tls := &TLSConfig{}
	if v, ok := obj["certificate"]; ok {
		tls.Certificate = p.string(ctx+".certificate", v)
	}
	if v, ok := obj["privatekey"]; ok {
		tls.PrivateKey = p.string(ctx+".privatekey", v)
	}
	if tls.Certificate == "" || tls.PrivateKey == "" {
		p.fail(ctx, "\"certificate\" and \"privatekey\" are both required")
	}
	return tls
}

// parseTarget interprets a route target string: a directory path, an http(s)
// URL, or a container:port reference.
func parseTarget(s string) (*Target, error) {
	if s == "" {
		return nil, fmt.Errorf("target is empty")
	}
	if strings.HasPrefix(s, "/") {
		return &Target{Path: s}, nil
	}
	if u, err := url.Parse(s); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return &Target{URL: s}, nil
	}
	name, portStr, found := strings.Cut(s, ":")
	if found && containerName.MatchString(name) {
		n, err := strconv.Atoi(portStr)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("target %q is not a valid port", s)
		}
		return &Target{Container: name, Port: uint16(n)}, nil
	}
	return nil, fmt.Errorf("target %q is not a path, an http(s) URL, or a container:port reference", s)
}

type durationJSON struct{ time.Duration }

func (d *durationJSON) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"5m\"")
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q (want e.g. \"5m\")", s)
	}
	d.Duration = v
	return nil
}

func (p *parser) fail(ctx, format string, args ...any) {
	if p.err == nil {
		p.err = fmt.Errorf("%s: %s", ctx, fmt.Sprintf(format, args...))
	}
}

func (p *parser) object(raw []byte, ctx string) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	p.decode(ctx, raw, &m)
	if m == nil {
		p.fail(ctx, "must be a JSON object")
		return map[string]json.RawMessage{}
	}
	out := make(map[string]json.RawMessage, len(m))
	first := make(map[string]string, len(m))
	for k, v := range m {
		ck := canonicalKey(k)
		if old, ok := out[ck]; ok {
			if !sameJSON(old, v) {
				p.fail(ctx, "keys %q and %q both mean %q but have different values", first[ck], k, ck)
			} else {
				p.warnings = append(p.warnings, fmt.Sprintf("%s: duplicate key %q (same value as %q)", ctx, k, first[ck]))
			}
		}
		out[ck] = v
		first[ck] = k
	}
	return out
}

func (p *parser) known(ctx string, obj map[string]json.RawMessage, names ...string) {
	for k := range obj {
		if !contains(names, k) {
			p.fail(ctx, "unknown key %q (known: %s)", k, strings.Join(names, ", "))
		}
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func canonicalKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '_' {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

func (p *parser) decode(ctx string, raw []byte, dst any) {
	if p.err != nil {
		return
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		p.fail(ctx, "%v", strings.TrimSpace(err.Error()))
		return
	}
	if dec.More() {
		p.fail(ctx, "unexpected trailing data")
	}
}

func (p *parser) string(ctx string, raw json.RawMessage) string {
	var s string
	p.decode(ctx, raw, &s)
	return s
}

func (p *parser) bool(ctx string, raw json.RawMessage) bool {
	var b bool
	p.decode(ctx, raw, &b)
	return b
}
