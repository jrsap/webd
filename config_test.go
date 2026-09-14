package webd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const sampleConfig = `{
  "443": {
    "tls": {
      "certificate": "/etc/webd/server.crt",
      "privateKey": "/etc/webd/server.key"
    },
    "routes": {
      "/": "/var/www",
      "/sub_path": "http://localhost:8080",
      "/sub_path_2": "tafels:3000"
    }
  },
  "containers": {
    "tafels": {
      "image": "quay.io/wvdschel/tafels:latest",
      "idleTimeout": "5m",
      "autostart": false
    }
  }
}`

func TestParseSampleConfig(t *testing.T) {
	cfg, warnings, err := ParseConfig([]byte(sampleConfig))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	port, ok := cfg.Ports[443]
	if !ok {
		t.Fatalf("no port 443 in %+v", cfg)
	}
	if port.TLS == nil || port.TLS.Certificate != "/etc/webd/server.crt" || port.TLS.PrivateKey != "/etc/webd/server.key" {
		t.Fatalf("bad tls: %+v", port.TLS)
	}
	if tr := port.Routes["/"]; tr == nil || tr.Path != "/var/www" {
		t.Fatalf("bad static route: %+v", tr)
	}
	if tr := port.Routes["/sub_path"]; tr == nil || tr.URL != "http://localhost:8080" {
		t.Fatalf("bad proxy route: %+v", tr)
	}
	if tr := port.Routes["/sub_path_2"]; tr == nil || tr.Container != "tafels" || tr.Port != 3000 {
		t.Fatalf("bad container route: %+v", tr)
	}
	c := cfg.Containers["tafels"]
	if c == nil || c.Image != "quay.io/wvdschel/tafels:latest" || c.IdleTimeout != 5*time.Minute || c.Autostart {
		t.Fatalf("bad container: %+v", c)
	}
}

func TestCanonicalizedKeys(t *testing.T) {
	cfg, _, err := ParseConfig([]byte(`{
	  "443": { "TLS": { "certificate": "c", "private_key": "k" },
	           "routes": { "/c": "tafel_x:80" } },
	  "containers": { "tafel_x": { "image": "i", "idle_timeout": "30s", "auto_start": true } }
	}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	p := cfg.Ports[443]
	if p.TLS.PrivateKey != "k" {
		t.Fatalf("private_key not accepted: %+v", p.TLS)
	}
	c := cfg.Containers["tafel_x"]
	if c.IdleTimeout != 30*time.Second || !c.Autostart {
		t.Fatalf("container not parsed: %+v", c)
	}
}

func TestRouteAndContainerNamesNotCanonicalized(t *testing.T) {
	cfg, _, err := ParseConfig([]byte(`{ "80": { "routes": { "/Foo_Bar": "My_Container:80" } }, "containers": { "My_Container": { "image": "i" } } }`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if _, ok := cfg.Ports[80].Routes["/Foo_Bar"]; !ok {
		t.Fatalf("route key mangled: %v", cfg.Ports[80].Routes)
	}
	if _, ok := cfg.Containers["My_Container"]; !ok {
		t.Fatalf("container name mangled: %v", cfg.Containers)
	}
}

func TestCollidingKeysError(t *testing.T) {
	_, _, err := ParseConfig([]byte(`{ "80": { "tls": { "certificate": "a", "privateKey": "x", "private_key": "y" }, "routes": { "/": "/s" } } }`))
	if err == nil {
		t.Fatal("want error on colliding keys with different values")
	}
}

func TestCollidingSameValueWarns(t *testing.T) {
	_, warnings, err := ParseConfig([]byte(`{ "80": { "tls": { "certificate": "a", "privateKey": "x", "private_key": "x" }, "routes": { "/": "/s" } } }`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "private_key") {
		t.Fatalf("want one warning naming private_key, got %v", warnings)
	}
}

func TestUnknownKey(t *testing.T) {
	_, _, err := ParseConfig([]byte(`{ "80": { "routes": { "/": "/s" }, "proxie": "http://x" } }`))
	if err == nil || !strings.Contains(err.Error(), "proxie") {
		t.Fatalf("want unknown-key error naming proxie, got %v", err)
	}
}

func TestTargetGrammar(t *testing.T) {
	refs := `"containers": { "tafels": { "image": "i" }, "my-app_2": { "image": "i" } }`
	for _, tt := range []struct {
		name, target string
		want         Target
	}{
		{"static", "/var/www", Target{Path: "/var/www"}},
		{"proxy", "http://localhost:8080", Target{URL: "http://localhost:8080"}},
		{"proxy https", "https://example.com", Target{URL: "https://example.com"}},
		{"container", "tafels:3000", Target{Container: "tafels", Port: 3000}},
		{"container dash underscore", "my-app_2:80", Target{Container: "my-app_2", Port: 80}},
	} {
		cfg, _, err := ParseConfig([]byte(`{ "80": { "routes": { "/": "` + tt.target + `" } }, ` + refs + ` }`))
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if got := cfg.Ports[80].Routes["/"]; *got != tt.want {
			t.Errorf("%s: got %+v want %+v", tt.name, *got, tt.want)
		}
	}
}

func TestTargetErrors(t *testing.T) {
	for _, tt := range []struct{ name, target string }{
		{"no port", "tafels"},
		{"unknown container", "nothere:1"},
		{"bad port", "tafels:x"},
		{"port zero", "tafels:0"},
		{"port range", "tafels:99999"},
		{"bad name", "not a name:1"},
		{"empty", ""},
		{"bare word", "somewhere"},
		{"ftp url", "ftp://host/file"},
		{"proxy no host", "http://"},
	} {
		b, _ := json.Marshal(tt.target)
		_, _, err := ParseConfig([]byte(`{ "80": { "routes": { "/": ` + string(b) + ` } }, "containers": { "tafels": { "image": "i" } } }`))
		if err == nil {
			t.Errorf("%s (%q): want error, got none", tt.name, tt.target)
		}
	}
}

func TestValidation(t *testing.T) {
	for _, tt := range []struct{ name, in string }{
		{"port out of range", `{ "99999": { "routes": { "/": "/s" } } }`},
		{"port not a number", `{ "http": { "routes": { "/": "/s" } } }`},
		{"route not a path", `{ "80": { "routes": { "foo": "/s" } } }`},
		{"target not a string", `{ "80": { "routes": { "/": { "path": "/s" } } } }`},
		{"no routes", `{ "80": { "routes": {} } }`},
		{"bad duration", `{ "80": { "routes": { "/": "c:80" } }, "containers": { "c": { "image": "i", "idleTimeout": "5 minutes" } } }`},
		{"container no image", `{ "80": { "routes": { "/": "c:80" } }, "containers": { "c": {} } }`},
		{"container bad name", `{ "80": { "routes": { "/": "/s" } }, "containers": { "bad name": { "image": "i" } } }`},
		{"tls half", `{ "80": { "tls": { "certificate": "c" }, "routes": { "/": "/s" } } }`},
		{"not json", `443: {}`},
	} {
		if _, _, err := ParseConfig([]byte(tt.in)); err == nil {
			t.Errorf("%s: want error, got none", tt.name)
		}
	}
}

func TestUnreferencedContainerWarns(t *testing.T) {
	_, warnings, err := ParseConfig([]byte(`{ "80": { "routes": { "/": "/s" } }, "containers": { "ghost": { "image": "i" } } }`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "ghost") {
		t.Fatalf("want warning naming ghost, got %v", warnings)
	}
}

func TestUnreferencedAutostartContainerNoWarning(t *testing.T) {
	_, warnings, err := ParseConfig([]byte(`{ "80": { "routes": { "/": "/s" } }, "containers": { "worker": { "image": "i", "autostart": true } } }`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("want no warnings, got %v", warnings)
	}
}

func TestDescriptionRejected(t *testing.T) {
	_, _, err := ParseConfig([]byte(`{ "80": { "routes": { "/": "/s" } }, "containers": { "c": { "image": "i", "description": "hello" } } }`))
	if err == nil {
		t.Fatal("description must be rejected")
	}
}

