package sub

import (
	"strings"
	"testing"

	"github.com/k1dory/vlr-main-agent/internal/wire"
)

func TestPickShortID(t *testing.T) {
	cur := []string{"aaaa", "bbbb", "cccc"}
	cases := []struct {
		name, userSID string
		current       []string
		want          string
	}{
		{"stored is still current -> keep", "bbbb", cur, "bbbb"},
		{"stored is stale -> first current", "deadbeef", cur, "aaaa"},
		{"empty stored -> first current", "", cur, "aaaa"},
		{"no snapshot -> best effort keeps stored", "deadbeef", nil, "deadbeef"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickShortID(c.userSID, c.current); got != c.want {
				t.Fatalf("pickShortID(%q, %v) = %q, want %q", c.userSID, c.current, got, c.want)
			}
		})
	}
}

// TestLinkUsesCurrentShortID is the regression for the reprovision bug: a user
// holding a short ID no longer in the node's set must still get a valid (current)
// sid in the link, so the subscription keeps working after a refresh.
func TestLinkUsesCurrentShortID(t *testing.T) {
	entry := wire.EntrySnapshot{
		Host: "158.160.205.160", Port: 443, SNI: "console.yandex.cloud",
		PublicKey: "PBK", Fingerprint: "randomized",
		ShortIDs: []string{"f61efa3f10f288f4", "aaaa1111bbbb2222"}, // current set
	}
	u := User{UUID: "1b66c81f", ShortID: "cb1b160ceb7c5386", Enabled: true} // stale short ID
	link := Link(entry, u)
	if strings.Contains(link, "sid=cb1b160ceb7c5386") {
		t.Fatalf("link still uses the stale short ID: %s", link)
	}
	if !strings.Contains(link, "sid=f61efa3f10f288f4") {
		t.Fatalf("link should fall back to a current short ID: %s", link)
	}
}
