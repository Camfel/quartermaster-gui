package main

import (
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
