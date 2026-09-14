package webd

import (
	"net/http"
	"sort"
	"strings"
)

// router dispatches requests by longest directory-aligned prefix: the key
// "/app" matches "/app" and "/app/x" but never "/app_x".
type router struct {
	entries []routeEntry
}

type routeEntry struct {
	prefix  string
	handler http.Handler
}

func newRouter(routes map[string]http.Handler) *router {
	rt := &router{}
	for path, h := range routes {
		path = strings.TrimSuffix(path, "/")
		if path == "" {
			path = "/"
		}
		rt.entries = append(rt.entries, routeEntry{prefix: path, handler: h})
	}
	sort.Slice(rt.entries, func(i, j int) bool {
		return len(rt.entries[i].prefix) > len(rt.entries[j].prefix)
	})
	return rt
}

func (rt *router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for _, e := range rt.entries {
		if e.prefix == "/" || r.URL.Path == e.prefix || strings.HasPrefix(r.URL.Path, e.prefix+"/") {
			e.handler.ServeHTTP(w, r)
			return
		}
	}
	http.NotFound(w, r)
}
