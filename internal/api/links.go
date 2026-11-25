package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// linksReq is the body of POST /v1/links.
type linksReq struct {
	Email string `json:"email"`
}

// guardLinks accepts the narrow AGENT_LINKS_TOKEN (given to ERP) or the full
// management token. Constant-time compare: ERP calls it from a shared network.
func (s *Server) guardLinks(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if !bearerIs(got, s.cfg.LinksToken) && !bearerIs(got, s.cfg.APIToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func bearerIs(header, token string) bool {
	if token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header), []byte("Bearer "+token)) == 1
}

// handleLinks returns a person's portal subscription link by email, issuing
// keys on first call — exactly what the SSO page /me shows that person
// (both go through portalLink). Idempotent: repeat calls and calls from the
// portal return the same URL.
//
//	POST /v1/links {"email":"i.ivanov@genomed.ru"}
//	→ 200 {"email":…, "sub_url":…, "servers":[…], "missing":[…]}
func (s *Server) handleLinks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if !s.cfg.PortalEnabled() {
		http.Error(w, "portal disabled (PUBLIC_SUB_BASE / PORTAL_NODE_IDS)", http.StatusServiceUnavailable)
		return
	}
	var req linksReq
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	email := strings.TrimSpace(req.Email)
	if !looksLikeEmail(email) {
		http.Error(w, "email required", http.StatusBadRequest)
		return
	}
	link, err := s.portalLink(r.Context(), email)
	if err != nil {
		s.log.Warn("links: lookup failed", "email", email, "err", err)
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	if link.SubURL == "" {
		http.Error(w, "provisioning failed on every node", http.StatusBadGateway)
		return
	}
	s.log.Info("links: link issued via API", "email", email)
	writeJSON(w, http.StatusOK, map[string]any{
		"email":   email,
		"sub_url": link.SubURL,
		"servers": link.Servers,
		"missing": link.Missing,
	})
}
