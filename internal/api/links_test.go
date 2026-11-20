package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k1dory/vlr-main-agent/internal/config"
)

func linksServer(cfg config.Config) http.Handler {
	s := &Server{cfg: cfg, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return s.guardLinks(s.handleLinks)
}

func doLinks(h http.Handler, method, auth, body string) int {
	r := httptest.NewRequest(method, "/v1/links", strings.NewReader(body))
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func TestLinksAuth(t *testing.T) {
	// Portal disabled => any request that passes auth gets 503, which makes
	// the guard observable without a database.
	h := linksServer(config.Config{APIToken: "admin", LinksToken: "erp"})
	cases := []struct {
		auth string
		want int
	}{
		{"", http.StatusUnauthorized},
		{"Bearer wrong", http.StatusUnauthorized},
		{"erp", http.StatusUnauthorized},
		{"Bearer erp", http.StatusServiceUnavailable},
		{"Bearer admin", http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		if got := doLinks(h, http.MethodPost, c.auth, `{"email":"a@b.ru"}`); got != c.want {
			t.Errorf("auth %q: got %d want %d", c.auth, got, c.want)
		}
	}
	// No links token configured: an empty bearer must not match it.
	h = linksServer(config.Config{APIToken: "admin"})
	if got := doLinks(h, http.MethodPost, "Bearer ", `{"email":"a@b.ru"}`); got != http.StatusUnauthorized {
		t.Errorf("empty links token accepted: %d", got)
	}
}

func TestLinksValidation(t *testing.T) {
	cfg := config.Config{LinksToken: "erp", SubBaseURL: "https://link.infrashark.tech",
		PortalNodes: []config.PortalNode{{ID: "ru-yc-msk"}}}
	h := linksServer(cfg)
	if got := doLinks(h, http.MethodGet, "Bearer erp", ""); got != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", got)
	}
	for _, body := range []string{`{`, `{}`, `{"email":"nope"}`, `{"email":"  "}`} {
		if got := doLinks(h, http.MethodPost, "Bearer erp", body); got != http.StatusBadRequest {
			t.Errorf("body %s: got %d want 400", body, got)
		}
	}
}
