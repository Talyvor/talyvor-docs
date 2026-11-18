package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// B67.6: a client cannot choose the address Docs sees by sending a forwarding header.
func TestBaseMiddleware_ForgedForwardingHeadersDoNotChangeRemoteAddr(t *testing.T) {
	r := chi.NewRouter()
	useBaseMiddleware(r)
	var seen string
	r.Get("/", func(w http.ResponseWriter, req *http.Request) { seen = req.RemoteAddr })

	for _, h := range []string{"True-Client-IP", "X-Real-IP", "X-Forwarded-For"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "203.0.113.7:4321"
		req.Header.Set(h, "198.51.100.99")
		r.ServeHTTP(httptest.NewRecorder(), req)
		if seen != "203.0.113.7:4321" {
			t.Errorf("%s: 198.51.100.99 forged, handler saw RemoteAddr %q, want the connection's 203.0.113.7:4321", h, seen)
		}
	}
}
