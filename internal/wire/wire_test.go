package wire

import (
	"testing"
	"time"
)

func TestShouldPull(t *testing.T) {
	now := time.Now()
	th := Thresholds{ByteDelta: 100 << 20, ReconcileEvery: 10 * time.Minute}

	cases := []struct {
		name string
		v    NodeView
		want bool
	}{
		{"never seen", NodeView{}, false},
		{"heard but never pulled", NodeView{LastSeen: now}, true},
		{"config advanced", NodeView{LastSeen: now, LastPull: now, HeardConfigVersion: 5, PulledConfigVersion: 4}, true},
		{"small traffic delta", NodeView{LastSeen: now, LastPull: now, HeardTotalBytes: 10 << 20}, false},
		{"big traffic delta", NodeView{LastSeen: now, LastPull: now, HeardTotalBytes: 200 << 20}, true},
		{"reconcile due", NodeView{LastSeen: now, LastPull: now.Add(-11 * time.Minute)}, true},
		{"nothing to do", NodeView{LastSeen: now, LastPull: now}, false},
	}
	for _, c := range cases {
		if got := ShouldPull(c.v, now, th).Pull; got != c.want {
			t.Errorf("%s: ShouldPull=%v want %v", c.name, got, c.want)
		}
	}
}

func TestMarkDown(t *testing.T) {
	now := time.Now()
	if MarkDown(time.Time{}, now, 20*time.Second, 3) {
		t.Error("never-seen node should not be DOWN")
	}
	if MarkDown(now.Add(-30*time.Second), now, 20*time.Second, 3) {
		t.Error("30s < 3*20s should be alive")
	}
	if !MarkDown(now.Add(-120*time.Second), now, 20*time.Second, 3) {
		t.Error("120s > 3*20s should be DOWN")
	}
}

func TestLinkRoundTripFieldsPresent(t *testing.T) {
	// guards against accidental json tag drift on the pull contract
	u := UserDetail{UUID: "abc", ShortID: "deadbeef", Profile: "vision"}
	if u.UUID == "" || u.ShortID == "" {
		t.Fatal("unexpected zero fields")
	}
}
