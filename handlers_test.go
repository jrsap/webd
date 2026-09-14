package webd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticHandler(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>hi</h1>"), 0o644)
	os.WriteFile(filepath.Join(dir, "data.bin"), []byte("\x7fELFstuff"), 0o644)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "f.txt"), []byte("nested"), 0o644)

	h := staticHandler(dir)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://h/", nil))
	if rec.Code != 200 || rec.Body.String() != "<h1>hi</h1>" {
		t.Fatalf("index: %d %q", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("want text/html, got %q", ct)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://h/sub/f.txt", nil))
	if rec.Code != 200 || rec.Body.String() != "nested" {
		t.Fatalf("nested: %d %q", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://h/missing", nil))
	if rec.Code != 404 {
		t.Fatalf("want 404, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://h/sub/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "f.txt") {
		t.Fatalf("want directory listing, got %d %q", rec.Code, rec.Body)
	}
}

func TestStaticContentTypeSniff(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "image"), []byte("\x89PNG\r\n\x1a\n"), 0o644)
	h := staticHandler(dir)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://h/image", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("magic-byte snif: got %q want image/png", ct)
	}
}

func TestStaticTraversalBlocked(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(filepath.Dir(dir), "outside.txt"), []byte("secret"), 0o644)
	h := staticHandler(dir)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://h/../outside.txt", nil)
	req.URL.Path = "/../outside.txt"
	h.ServeHTTP(rec, req)
	if rec.Code == 200 {
		t.Fatal("traversal must not serve files outside the directory")
	}
}

func TestProxyHandler(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "1")
		w.Write([]byte(r.URL.Path + "|" + r.Header.Get("X-Probe")))
	}))
	defer backend.Close()

	h := proxyHandler(backend.URL)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://webd/under/path", strings.NewReader("body"))
	req.Header.Set("X-Probe", "yes")
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.String() != "/under/path|yes" {
		t.Fatalf("got %d %q", rec.Code, rec.Body)
	}
	if rec.Header().Get("X-Backend") != "1" {
		t.Errorf("backend header not forwarded")
	}
}

func TestProxyHandlerUnreachable(t *testing.T) {
	h := proxyHandler("http://127.0.0.1:1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://webd/x", nil))
	if rec.Code != 502 {
		t.Fatalf("want 502, got %d", rec.Code)
	}
}
