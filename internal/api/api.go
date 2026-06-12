// Package api is the agent's HTTP surface: it ingests node heartbeats, lets an
// operator register nodes, manages ALL users/subscriptions centrally (proxying
// create/delete to the owning node, which holds the Xray config), and serves the
// base64 subscription for any user from PostgreSQL.
package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/k1dory/vlr-main-agent/internal/collector"
	"github.com/k1dory/vlr-main-agent/internal/config"
	"github.com/k1dory/vlr-main-agent/internal/store"
	"github.com/k1dory/vlr-main-agent/internal/sub"
	"github.com/k1dory/vlr-main-agent/internal/wire"
)

// Server is the agent HTTP API.
type Server struct {
	cfg   config.Config
	pg    *store.Store
	coll  *collector.Collector
	log   *slog.Logger
	httpc *http.Client
}

// New builds the API server.
func New(cfg config.Config, pg *store.Store, coll *collector.Collector, log *slog.Logger) *Server {
	return &Server{cfg: cfg, pg: pg, coll: coll, log: log, httpc: cfg.NodeHTTPClient(15 * time.Second)}
}

// Handler builds the mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/heartbeat", s.handleHeartbeat)       // node -> agent
	mux.HandleFunc("/v1/nodes", s.guard(s.handleNodes))      // operator
	mux.HandleFunc("/v1/users", s.guard(s.handleUsers))      // operator
	mux.HandleFunc("/v1/users/", s.guard(s.handleUserByRef)) // operator (DELETE)
	mux.HandleFunc("/sub/", s.handleSub)                     // client subscription URL
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	return mux
}

// guard enforces the management API token.
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	want := "Bearer " + s.cfg.APIToken
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIToken == "" || r.Header.Get("Authorization") != want {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// --- heartbeat ingest ------------------------------------------------------

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	var hb wire.Heartbeat
	if err := json.Unmarshal(body, &hb); err != nil || hb.NodeID == "" {
		http.Error(w, "bad heartbeat", http.StatusBadRequest)
		return
	}
	// Verify the node's heartbeat token if one is registered.
	reg, ok, _ := s.pg.Reg(r.Context(), hb.NodeID)
	if !ok {
		http.Error(w, "unknown node", http.StatusForbidden)
		return
	}
	if reg.HeartbeatToken != "" && r.Header.Get("Authorization") != "Bearer "+reg.HeartbeatToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := s.coll.Ingest(r.Context(), hb); err != nil {
		s.log.Warn("ingest heartbeat failed", "node", hb.NodeID, "err", err)
		http.Error(w, "store", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- node registry ---------------------------------------------------------

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.pg.ListNodes(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, list)
	case http.MethodPost:
		var reg store.NodeReg
		if err := readJSON(r, &reg); err != nil || reg.NodeID == "" {
			http.Error(w, "bad node (node_id required)", http.StatusBadRequest)
			return
		}
		if err := s.pg.RegisterNode(r.Context(), reg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"registered": reg.NodeID})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// --- users (central) -------------------------------------------------------

type createReq struct {
	NodeID     string `json:"node_id"`
	TelegramID int64  `json:"telegram_id"`
	Email      string `json:"email"`
	ID         string `json:"id"`
	Profile    string `json:"profile"`    // "vision" | ""
	Label      string `json:"label"`      // per-key display name -> vless:// #fragment ("For Learn 7d")
	Title      string `json:"title"`      // per-user Profile-Title override (else AGENT_SUB_TITLE)
	ExpiresIn  string `json:"expires_in"` // "7d" | "30d" | "12h" | "" (never); takes precedence
	ExpiresAt  string `json:"expires_at"` // RFC3339 absolute expiry, alternative to expires_in
}

// parseExpiry resolves the requested expiry into an absolute time (nil = never).
// Accepts "<N>d" (days) and anything time.ParseDuration understands for the
// relative form, or an RFC3339 timestamp for the absolute form.
func parseExpiry(in, at string) (*time.Time, error) {
	if in != "" {
		var d time.Duration
		var err error
		if strings.HasSuffix(in, "d") {
			var days int
			if _, err = fmt.Sscanf(in, "%dd", &days); err == nil {
				d = time.Duration(days) * 24 * time.Hour
			}
		} else {
			d, err = time.ParseDuration(in)
		}
		if err != nil {
			return nil, fmt.Errorf("bad expires_in %q (use 7d, 12h, ...)", in)
		}
		t := time.Now().Add(d).UTC()
		return &t, nil
	}
	if at != "" {
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return nil, fmt.Errorf("bad expires_at %q (use RFC3339)", at)
		}
		return &t, nil
	}
	return nil, nil
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := s.pg.ListUsers(r.Context(), r.URL.Query().Get("node_id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, rows)
	case http.MethodPost:
		s.createUser(w, r)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// createUser proxies the create to the owning node (which mutates the Xray
// config), then resyncs that node so the central PG/ClickHouse reflect the new
// key, and returns the node's link + base64 subscription.
func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := readJSON(r, &req); err != nil || req.NodeID == "" {
		http.Error(w, "node_id required", http.StatusBadRequest)
		return
	}
	expiresAt, err := parseExpiry(req.ExpiresIn, req.ExpiresAt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	reg, ok, err := s.pg.Reg(r.Context(), req.NodeID)
	if err != nil || !ok {
		http.Error(w, "unknown node", http.StatusNotFound)
		return
	}
	if reg.UserAPIURL == "" {
		http.Error(w, "node has no user_api_url registered", http.StatusBadRequest)
		return
	}
	body, _ := json.Marshal(wire.CreateUserReq{
		TelegramID: req.TelegramID, Email: req.Email, ID: req.ID, Profile: req.Profile,
	})
	resp, err := s.nodeCall(r.Context(), http.MethodPost,
		strings.TrimRight(reg.UserAPIURL, "/")+"/v1/users", reg.UserAPIToken, body)
	if err != nil {
		http.Error(w, "node call failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		http.Error(w, "node rejected: "+string(b), http.StatusBadGateway)
		return
	}
	var cr wire.CreateUserResp
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		http.Error(w, "bad node response", http.StatusBadGateway)
		return
	}
	// Insert the row immediately so it is listable and its /sub URL works even if
	// the resync pull below fails; the pull then fills traffic/short_id detail.
	subToken, ierr := s.pg.InsertUser(r.Context(), store.UserRow{
		UUID: cr.UUID, NodeID: req.NodeID, Email: req.Email,
		TelegramID: req.TelegramID, ExternalID: req.ID, Profile: req.Profile,
		Label: req.Label, Title: req.Title, ExpiresAt: expiresAt,
	})
	if ierr != nil {
		s.log.Warn("insert user after create failed", "uuid", cr.UUID, "err", ierr)
	}
	if err := s.coll.Pull(r.Context(), req.NodeID); err != nil {
		s.log.Warn("resync pull after create failed", "node", req.NodeID, "err", err)
	}
	resp2 := map[string]any{
		"uuid":         cr.UUID,
		"link":         cr.Link,
		"subscription": cr.Subscription,
		"sub_token":    subToken,
		"sub_path":     "/sub/" + subToken,
	}
	if expiresAt != nil {
		resp2["expires_at"] = expiresAt.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusCreated, resp2)
}

func (s *Server) handleUserByRef(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimPrefix(r.URL.Path, "/v1/users/")
	if ref == "" {
		http.Error(w, "ref required", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	u, ok, err := s.pg.FindUser(r.Context(), ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	reg, ok, _ := s.pg.Reg(r.Context(), u.NodeID)
	if ok && reg.UserAPIURL != "" {
		resp, err := s.nodeCall(r.Context(), http.MethodDelete,
			strings.TrimRight(reg.UserAPIURL, "/")+"/v1/users/"+u.UUID, reg.UserAPIToken, nil)
		if err != nil {
			http.Error(w, "node call failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
			http.Error(w, "node rejected delete", http.StatusBadGateway)
			return
		}
	}
	if err := s.pg.DeleteUser(r.Context(), u.UUID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": u.UUID})
}

// --- subscription -----------------------------------------------------------

// handleSub serves the base64 subscription for a user resolved by its
// high-entropy sub_token (or uuid) ONLY — never a guessable email/telegram id,
// which would let anyone enumerate other users' keys. This is the central
// subscription URL a client app polls; front it with TLS in production.
func (s *Server) handleSub(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/sub/")
	if token == "" {
		http.Error(w, "token required", http.StatusBadRequest)
		return
	}
	u, ok, err := s.pg.SubUser(r.Context(), token)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok || !u.Enabled {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if u.ExpiresAt != nil && time.Now().After(*u.ExpiresAt) {
		http.Error(w, "subscription expired", http.StatusGone) // reaper will delete it shortly
		return
	}
	su := sub.User{
		UUID: u.UUID, Email: u.Email, TelegramID: u.TelegramID, ExternalID: u.ExternalID,
		ShortID: u.ShortID, Profile: u.Profile, Label: u.Label, Enabled: u.Enabled,
	}
	body := sub.Stream(u.Entry, []sub.User{su})

	// Presentation headers every mainstream client (v2rayNG/Hiddify/Clash) reads.
	title := u.Title
	if title == "" {
		title = s.cfg.SubTitle
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if title != "" {
		// base64 form is the portable way to carry non-ASCII (Cyrillic/emoji).
		w.Header().Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte(title)))
	}
	if s.cfg.SubUpdateHrs > 0 {
		w.Header().Set("Profile-Update-Interval", strconv.Itoa(s.cfg.SubUpdateHrs))
	}
	if s.cfg.SubSupportURL != "" {
		w.Header().Set("Support-URL", s.cfg.SubSupportURL)
	}
	// Subscription-Userinfo: used traffic + expiry. expire is a unix timestamp
	// (0 = unlimited); the client renders the remaining-days / expiry date from it.
	var expire int64
	if u.ExpiresAt != nil {
		expire = u.ExpiresAt.Unix()
	}
	w.Header().Set("Subscription-Userinfo",
		fmt.Sprintf("upload=0; download=%d; total=0; expire=%d", u.RxBytes+u.TxBytes, expire))

	_, _ = w.Write([]byte(body))
}

// --- helpers ---------------------------------------------------------------

func (s *Server) nodeCall(ctx context.Context, method, url, token string, body []byte) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return s.httpc.Do(req)
}

func readJSON(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
