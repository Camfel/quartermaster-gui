package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestShortImage(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"nginx:latest", "nginx:latest"},
		{"lscr.io/linuxserver/jellyfin:latest", "jellyfin:latest"},
		{"docker.io/library/postgres:16-alpine", "postgres:16-alpine"},
		{"very/long/path/to/some/image/with/many/segments:tag", "segments:tag"},
	}
	for _, c := range cases {
		got := shortImage(c.in)
		if got != c.want {
			t.Errorf("shortImage(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHealthBadge(t *testing.T) {
	noHC := healthBadge(nil)
	if string(noHC) == "" {
		t.Error("expected non-empty badge for nil healthcheck")
	}

	h := true
	ok := healthBadge(&h)
	if string(ok) == "" {
		t.Error("expected non-empty badge for healthy")
	}

	h = false
	fail := healthBadge(&h)
	if string(fail) == "" {
		t.Error("expected non-empty badge for unhealthy")
	}
}

func TestSince(t *testing.T) {
	// nil time
	if since(nil) != "never" {
		t.Errorf("since(nil) = %q, want %q", since(nil), "never")
	}
}

func TestValidServiceName(t *testing.T) {
	good := []string{"sonarr", "golf-super-coach", "node_exporter", "a", "svc.1", "UPPER-case"}
	for _, n := range good {
		if !validServiceName(n) {
			t.Errorf("validServiceName(%q) = false, want true", n)
		}
	}
	// Rejected: path traversal, separators, spaces, HTML, empty.
	bad := []string{"", "..", "../etc", "a/b", "a b", "<script>", "a\x00b", "-leading"}
	for _, n := range bad {
		if validServiceName(n) {
			t.Errorf("validServiceName(%q) = true, want false", n)
		}
	}
}

func TestRequireSameOrigin(t *testing.T) {
	mk := func(hdr map[string]string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://dash.boon.blue/htmx/reconcile", nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}

	// Allowed: same-origin browser request, and header-less non-browser clients.
	allowed := []map[string]string{
		{},
		{"Sec-Fetch-Site": "same-origin"},
		{"Sec-Fetch-Site": "none"},
		{"Origin": "http://dash.boon.blue"},
		{"Referer": "http://dash.boon.blue/"},
	}
	for _, h := range allowed {
		if err := requireSameOrigin(mk(h)); err != nil {
			t.Errorf("headers %v rejected, want allowed: %v", h, err)
		}
	}

	// Rejected: cross-site browser requests.
	blocked := []map[string]string{
		{"Sec-Fetch-Site": "cross-site"},
		{"Sec-Fetch-Site": "same-site"},
		{"Origin": "https://evil.example"},
		{"Referer": "https://evil.example/page"},
	}
	for _, h := range blocked {
		if err := requireSameOrigin(mk(h)); err == nil {
			t.Errorf("headers %v allowed, want rejected", h)
		}
	}
}

func TestGuardPost(t *testing.T) {
	// GET is refused with 405.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://dash.boon.blue/htmx/reconcile", nil)
	if guardPost(rec, req) {
		t.Fatal("guardPost allowed a GET")
	}
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}

	// Cross-origin POST is refused with 403.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "http://dash.boon.blue/htmx/reconcile", nil)
	req.Header.Set("Origin", "https://evil.example")
	if guardPost(rec, req) {
		t.Fatal("guardPost allowed a cross-origin POST")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	// Same-origin POST is allowed through.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "http://dash.boon.blue/htmx/reconcile", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if !guardPost(rec, req) {
		t.Fatal("guardPost rejected a same-origin POST")
	}
}
