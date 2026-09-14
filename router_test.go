package webd

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func markHandler(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(name))
	})
}

func TestRouterMatching(t *testing.T) {
	rt := newRouter(map[string]http.Handler{
		"/":          markHandler("root"),
		"/sub_path":  markHandler("sub"),
		"/sub_path2": markHandler("sub2"),
		"/a/b":       markHandler("ab"),
	})
	for _, tt := range []struct{ path, want string }{
		{"/", "root"},
		{"/x", "root"},
		{"/sub_path", "sub"},
		{"/sub_path/", "sub"},
		{"/sub_path/x/y", "sub"},
		{"/sub_path_b", "root"},
		{"/sub_path2", "sub2"},
		{"/sub_path2/x", "sub2"},
		{"/a", "root"},
		{"/a/b", "ab"},
		{"/a/b/c", "ab"},
		{"/a/bc", "root"},
	} {
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://h"+tt.path, nil))
		if rec.Code == http.StatusNotFound {
			if tt.want != "" {
				t.Errorf("%s: unexpected 404", tt.path)
			}
			continue
		}
		if got := rec.Body.String(); got != tt.want {
			t.Errorf("%s: got %q want %q", tt.path, got, tt.want)
		}
	}
}

func TestRouterTrailingSlashKey(t *testing.T) {
	rt := newRouter(map[string]http.Handler{"/app/": markHandler("app")})
	for _, path := range []string{"/app", "/app/", "/app/x"} {
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://h"+path, nil))
		if got := rec.Body.String(); got != "app" {
			t.Errorf("%s: got %q want app", path, got)
		}
	}
}

func TestRouterNoMatch(t *testing.T) {
	rt := newRouter(map[string]http.Handler{"/app": markHandler("app")})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://h/other", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}
