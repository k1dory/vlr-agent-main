package api

import (
	"strings"
	"testing"

	"github.com/k1dory/vlr-main-agent/internal/config"
)

func TestLooksLikeEmail(t *testing.T) {
	ok := []string{"a@b.ru", "i.ivanov@genomed.ru"}
	bad := []string{"", "nope", "@x.ru", "a@", "a b@x.ru", "a@x.ru\n"}
	for _, e := range ok {
		if !looksLikeEmail(e) {
			t.Errorf("looksLikeEmail(%q) = false, want true", e)
		}
	}
	for _, e := range bad {
		if looksLikeEmail(e) {
			t.Errorf("looksLikeEmail(%q) = true, want false", e)
		}
	}
}

func TestSubURL(t *testing.T) {
	cases := []struct {
		base, token, want string
	}{
		{"https://link.infrashark.tech", "abc123", "https://link.infrashark.tech/sub/abc123"},
		{"https://link.infrashark.tech/", "abc123", "https://link.infrashark.tech/sub/abc123"},
		{"", "abc123", ""},
		{"https://link.infrashark.tech", "", ""},
	}
	for _, c := range cases {
		s := &Server{cfg: config.Config{SubBaseURL: c.base}}
		if got := s.subURL(c.token); got != c.want {
			t.Errorf("subURL(base=%q, token=%q) = %q, want %q", c.base, c.token, got, c.want)
		}
	}
}

func TestQRSVGNonEmpty(t *testing.T) {
	svg := string(qrSVG("https://link.infrashark.tech/sub/deadbeefdeadbeefdeadbeefdeadbeef"))
	if !strings.HasPrefix(svg, "<svg") || !strings.Contains(svg, "<path") {
		t.Fatal("qrSVG should produce an <svg> with a module path")
	}
	if qrSVG("") != "" {
		t.Fatal("qrSVG(\"\") must be empty")
	}
}
