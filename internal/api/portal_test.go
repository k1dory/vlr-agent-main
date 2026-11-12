package api

import (
	"strings"
	"testing"
	"time"

	"github.com/k1dory/vlr-main-agent/internal/config"
	"github.com/k1dory/vlr-main-agent/internal/store"
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

func TestParsePortalNodes(t *testing.T) {
	got := config.ParsePortalNodes(" ru-yc-msk=Основной , ru-yc-msk-02=Резервный,ru-yc-msk,,x ", "legacy")
	want := []config.PortalNode{{ID: "ru-yc-msk", Name: "Основной"}, {ID: "ru-yc-msk-02", Name: "Резервный"}, {ID: "x"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %+v want %+v", got, want)
		}
	}
	if l := config.ParsePortalNodes("", "ru-yc-msk"); len(l) != 1 || l[0].ID != "ru-yc-msk" {
		t.Fatalf("legacy fallback: %+v", l)
	}
	if l := config.ParsePortalNodes("", ""); len(l) != 0 {
		t.Fatalf("empty: %+v", l)
	}
}

func TestTokensByNodeKeepsOldestPerNode(t *testing.T) {
	m := tokensByNode([]store.UserRow{
		{NodeID: "a", SubToken: "t1"}, {NodeID: "a", SubToken: "t2"}, {NodeID: "b", SubToken: ""}, {NodeID: "c", SubToken: "t3"},
	})
	if len(m) != 2 || m["a"] != "t1" || m["c"] != "t3" {
		t.Fatalf("got %v", m)
	}
}

func TestPortalRendersServers(t *testing.T) {
	var b strings.Builder
	err := portalTmpl.Execute(&b, portalData{Email: "a@b.ru", Title: "VPN", SubURL: "https://l/sub/one",
		QR: qrSVG("https://l/sub/one"), Servers: []string{"Основной", "Резервный"}, Missing: []string{"Третий"}})
	if err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"Основной", "Резервный", "https://l/sub/one", "2 сервера", "Пока недоступны", "Третий"} {
		if !strings.Contains(out, want) {
			t.Errorf("portal page missing %q", want)
		}
	}
}

func TestSubKeys(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	nodes := []config.PortalNode{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	req := store.UserRow{UUID: "u-a", NodeID: "a", Enabled: true}
	all := []store.UserRow{
		req,
		{UUID: "u-b-expired", NodeID: "b", Enabled: true, ExpiresAt: &past},
		{UUID: "u-b", NodeID: "b", Enabled: true},
		{UUID: "u-b2", NodeID: "b", Enabled: true},
		{UUID: "u-c-off", NodeID: "c", Enabled: false},
		{UUID: "u-x", NodeID: "x", Enabled: true}, // not a portal node
	}
	got := subKeys(req, all, nodes, now)
	var ids []string
	for _, k := range got {
		ids = append(ids, k.UUID)
	}
	if strings.Join(ids, ",") != "u-a,u-b" {
		t.Fatalf("got %v", ids)
	}
	// Requested key on a non-portal node stays first, siblings follow.
	got = subKeys(store.UserRow{UUID: "u-x", NodeID: "x", Enabled: true}, all, nodes, now)
	ids = ids[:0]
	for _, k := range got {
		ids = append(ids, k.UUID)
	}
	if strings.Join(ids, ",") != "u-x,u-a,u-b" {
		t.Fatalf("non-portal: got %v", ids)
	}
}
