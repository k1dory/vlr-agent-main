// Package wire defines the on-the-wire types vlr nodes speak, plus the pure
// delta-pull decision. This agent talks to nodes over HTTP/JSON only, so it
// owns its own copy of these DTOs (no shared Go module with vlr) — the JSON tags
// are the contract. Keep them byte-compatible with vlr's internal/protocol.
package wire

import "time"

// Heartbeat is the cheap, fixed-shape liveness+summary push from a node.
type Heartbeat struct {
	NodeID        string `json:"node_id"`
	Seq           uint64 `json:"seq"`
	SentUnix      int64  `json:"sent_unix"`
	Healthy       bool   `json:"healthy"`
	CascadeUp     bool   `json:"cascade_up"`
	UserCount     int    `json:"user_count"`
	ConfigVersion int64  `json:"config_version"`
	TotalBytes    int64  `json:"total_bytes"`
}

// EntrySnapshot is the PUBLIC Reality entry material a node exposes so the agent
// can rebuild client vless:// links. Never contains the server private key.
type EntrySnapshot struct {
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	SNI         string   `json:"sni"`
	PublicKey   string   `json:"public_key"`
	Fingerprint string   `json:"fingerprint"`
	ShortIDs    []string `json:"short_ids"`
}

// UserDetail is one user's full record as returned by a node's /v1/pull.
type UserDetail struct {
	UUID       string `json:"uuid"`
	Email      string `json:"email"`
	TelegramID int64  `json:"telegram_id"`
	ExternalID string `json:"external_id"`
	Profile    string `json:"profile"`
	ShortID    string `json:"short_id"`
	Enabled    bool   `json:"enabled"`
	RxBytes    int64  `json:"rx_bytes"`
	TxBytes    int64  `json:"tx_bytes"`
}

// PullResponse is the heavy detail a node returns from GET /v1/pull.
type PullResponse struct {
	NodeID        string        `json:"node_id"`
	ConfigVersion int64         `json:"config_version"`
	Entry         EntrySnapshot `json:"entry"`
	Users         []UserDetail  `json:"users"`
	XrayConfig    string        `json:"xray_config,omitempty"`
}

// CreateUserReq is the body the node's POST /v1/users accepts (all optional).
type CreateUserReq struct {
	TelegramID int64  `json:"telegram_id,omitempty"`
	ID         string `json:"id,omitempty"`
	Email      string `json:"email,omitempty"`
	Profile    string `json:"profile,omitempty"`
}

// CreateUserResp is what the node's POST /v1/users returns.
type CreateUserResp struct {
	UUID         string `json:"uuid"`
	Link         string `json:"link"`
	Subscription string `json:"subscription"`
}

// NodeView is the agent's running scheduling state for one node. It mirrors the
// authoritative columns in the `nodes` table and drives ShouldPull.
type NodeView struct {
	NodeID    string
	LastSeen  time.Time
	LastSeq   uint64
	Healthy   bool
	CascadeUp bool
	Down      bool

	PulledConfigVersion int64
	PulledTotalBytes    int64
	LastPull            time.Time

	HeardConfigVersion int64
	HeardTotalBytes    int64
}

// PullDecision explains why (or why not) a pull should happen now.
type PullDecision struct {
	Pull   bool
	Reason string
}

// Thresholds parameterises ShouldPull. Zero values fall back to defaults.
type Thresholds struct {
	ByteDelta      int64
	ReconcileEvery time.Duration
}

func (t Thresholds) withDefaults() Thresholds {
	if t.ByteDelta <= 0 {
		t.ByteDelta = 256 << 20
	}
	if t.ReconcileEvery <= 0 {
		t.ReconcileEvery = 10 * time.Minute
	}
	return t
}

// ShouldPull is the pure delta-pull decision (identical semantics to vlr's
// protocol.ShouldPull): pull when heard state has drifted from pulled state in a
// way that matters, when the node just recovered, or when a reconcile is due.
func ShouldPull(v NodeView, now time.Time, th Thresholds) PullDecision {
	th = th.withDefaults()
	// Never heard from the node yet — nothing to pull, and the reconcile timer
	// must not fire against a zero LastPull.
	if v.LastSeen.IsZero() {
		return PullDecision{false, ""}
	}
	if v.LastPull.IsZero() {
		return PullDecision{true, "initial baseline pull"}
	}
	if v.HeardConfigVersion > v.PulledConfigVersion {
		return PullDecision{true, "config_version advanced"}
	}
	if v.HeardTotalBytes-v.PulledTotalBytes >= th.ByteDelta {
		return PullDecision{true, "traffic delta over threshold"}
	}
	if now.Sub(v.LastPull) >= th.ReconcileEvery {
		return PullDecision{true, "reconcile interval elapsed"}
	}
	return PullDecision{false, ""}
}

// MarkDown reports whether a node should be considered DOWN.
func MarkDown(lastSeen, now time.Time, interval time.Duration, allowedMisses int) bool {
	if lastSeen.IsZero() {
		return false
	}
	if allowedMisses <= 0 {
		allowedMisses = 3
	}
	if interval <= 0 {
		interval = 20 * time.Second
	}
	return now.Sub(lastSeen) > time.Duration(allowedMisses)*interval
}
