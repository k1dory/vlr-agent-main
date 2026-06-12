// Package sub rebuilds the VLESS+Reality share link and base64 subscription for
// a user, centrally on the agent. The output is byte-for-byte the same shape as
// the link the node itself issues (vlr's internal/subscription), so a centrally
// served subscription and a node-served one are interchangeable.
package sub

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"github.com/k1dory/vlr-main-agent/internal/wire"
)

// User is the minimal per-user identity needed to render a link.
type User struct {
	UUID       string
	Email      string
	TelegramID int64
	ExternalID string
	ShortID    string
	Profile    string // "vision" => xtls-rprx-vision; empty => plain Reality
	Label      string // explicit display name -> vless:// #fragment (e.g. "For Learn 7d")
	Enabled    bool
}

// Link renders one vless:// share URL against an entry snapshot.
func Link(entry wire.EntrySnapshot, u User) string {
	q := url.Values{}
	q.Set("type", "tcp")
	q.Set("security", "reality")
	q.Set("sni", entry.SNI)
	q.Set("fp", entry.Fingerprint)
	q.Set("pbk", entry.PublicKey)
	sid := u.ShortID
	if sid == "" && len(entry.ShortIDs) > 0 {
		sid = entry.ShortIDs[0]
	}
	q.Set("sid", sid)
	if u.Profile == "vision" {
		q.Set("flow", "xtls-rprx-vision")
	}
	q.Set("spx", "/")

	return fmt.Sprintf("vless://%s@%s:%d?%s#%s",
		u.UUID, entry.Host, entry.Port, q.Encode(), url.PathEscape(label(u)))
}

// label picks a human tag for the link: an explicit Label wins, else email, else
// tg<id>, else external id, else a short uuid. Never empty.
func label(u User) string {
	if u.Label != "" {
		return u.Label
	}
	switch {
	case u.Email != "":
		return u.Email
	case u.TelegramID != 0:
		return fmt.Sprintf("tg%d", u.TelegramID)
	case u.ExternalID != "":
		return u.ExternalID
	default:
		if len(u.UUID) >= 8 {
			return u.UUID[:8]
		}
		return u.UUID
	}
}

// Stream builds the base64 subscription body for a set of users on one entry.
func Stream(entry wire.EntrySnapshot, users []User) string {
	lines := make([]string, 0, len(users))
	for _, u := range users {
		if !u.Enabled {
			continue
		}
		lines = append(lines, Link(entry, u))
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n")))
}

// MultiStream builds one base64 subscription spanning several entry nodes, so a
// client gets a single link covering every region and the app fails over.
func MultiStream(entries []wire.EntrySnapshot, usersPerEntry map[string][]User) string {
	var lines []string
	for _, e := range entries {
		for _, u := range usersPerEntry[e.Host] {
			if !u.Enabled {
				continue
			}
			lines = append(lines, Link(e, u))
		}
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n")))
}
