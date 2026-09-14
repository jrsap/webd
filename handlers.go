package webd

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
)

func staticHandler(dir string) http.Handler {
	return http.FileServerFS(os.DirFS(dir))
}

func proxyHandler(target string) http.Handler {
	u, err := url.Parse(target)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "bad proxy target", http.StatusBadGateway)
		})
	}
	return httputil.NewSingleHostReverseProxy(u)
}
