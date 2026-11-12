package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// authentikEvent is the (customized) webhook body Authentik POSTs on a user
// deactivation/deletion — shaped by the Notification Webhook Mapping in
// docs/AUTHENTIK.md. is_active is a pointer so an omitted field (a delete) reads
// as "not active" → revoke.
type authentikEvent struct {
	Action   string `json:"action"`    // "model_updated" | "model_deleted"
	Email    string `json:"email"`     // resolved by the Authentik body mapping
	IsActive *bool  `json:"is_active"` // false/absent on deactivation or delete
}

// handleAuthentikEvent is the event-driven auto-revoke webhook. Authentik calls it
// the instant a user is deactivated or deleted; the agent deletes that
// portal-provisioned user everywhere (owning node + PostgreSQL). No polling.
//
// Guarded by Authorization: Bearer <cfg.AuthentikWebhookSecret>. Disabled (404,
// via the guard) unless a secret is configured. Always 200 on a well-formed
// request (even unknown users) so Authentik does not retry-storm.
func (s *Server) handleAuthentikEvent(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AuthentikWebhookSecret == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.cfg.AuthentikWebhookSecret {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var ev authentikEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	email := strings.TrimSpace(ev.Email)
	active := ev.IsActive != nil && *ev.IsActive
	if ev.Action != "model_deleted" && active {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": false, "reason": "still active"})
		return
	}
	if email == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": false, "reason": "no email"})
		return
	}
	revoked := s.revokeAuthentikUser(r.Context(), email)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": revoked})
}

// revokeAuthentikUser deletes every portal-provisioned key with this email (one
// per portal node). It only touches users the portal created (external_id==portalSource)
// so a manually/bot-created user with the same email is never collateral. Returns
// whether anything was removed (idempotent).
func (s *Server) revokeAuthentikUser(ctx context.Context, email string) bool {
	users, err := s.pg.UsersByEmail(ctx, email)
	if err != nil {
		s.log.Warn("authentik webhook: lookup failed", "email", email, "err", err)
		return false
	}
	revoked := false
	for _, u := range users {
		if u.ExternalID != portalSource {
			continue
		}
		if err := s.deleteUserEverywhere(ctx, u); err != nil {
			s.log.Warn("authentik webhook: delete failed", "email", email, "node", u.NodeID, "err", err)
			continue
		}
		s.log.Info("authentik webhook: revoked user", "email", email, "uuid", u.UUID, "node", u.NodeID)
		revoked = true
	}
	return revoked
}
