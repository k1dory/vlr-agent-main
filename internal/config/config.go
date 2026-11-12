// Package config loads the agent configuration from the environment. The agent
// is containerized, so env vars (12-factor) are the natural source; defaults are
// chosen so `docker compose up` works with the bundled compose .env.
package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the full agent configuration.
type Config struct {
	Listen   string // HTTP API listen address, e.g. 0.0.0.0:8443
	APIToken string // guards management endpoints (POST/DELETE /v1/users, /v1/nodes)

	PostgresDSN    string // postgres://user:pass@host:5432/db?sslmode=disable
	ClickHouseAddr string // host:9000 (native protocol)
	ClickHouseDB   string
	ClickHouseUser string
	ClickHousePass string

	// Subscription presentation (HTTP headers clients read).
	SubTitle      string // default Profile-Title, e.g. "🎓 Обучение у Александры Олейник"
	SubUpdateHrs  int    // Profile-Update-Interval (hours)
	SubSupportURL string // Support-URL shown in the client

	// --- self-service portal (GET /me, behind Authentik forward-auth) ---
	// SubBaseURL is the public origin that fronts THIS agent's /sub endpoint, e.g.
	// https://link.infrashark.tech. The portal hands clients <SubBaseURL>/sub/<token>.
	SubBaseURL string
	// PortalNodes are the nodes every portal user gets a key on; all keys ride in
	// one subscription (named after the node) so the user switches in the client.
	// From PORTAL_NODE_IDS="id=Name,id2=Name2" (Name optional), or the legacy
	// single PORTAL_NODE_ID. Empty => portal disabled.
	PortalNodes []PortalNode
	// PortalHeader is the request header carrying the authenticated email, set by
	// the Authentik forward-auth proxy. Default "X-Authentik-Email".
	PortalHeader string
	// PortalProfile is the vlr profile portal users get ("" plain Reality, "vision").
	PortalProfile string

	// AuthentikWebhookSecret is the bearer the Authentik notification webhook must
	// present to POST /v1/authentik/event (event-driven auto-revoke). Empty => the
	// webhook endpoint is disabled.
	AuthentikWebhookSecret string

	// Delta-pull tuning (mirrors vlr's main config).
	PullThresholdBytes int64
	ReconcileEvery     time.Duration
	DownAfterMisses    int
	HeartbeatInterval  time.Duration

	// TLS trust for agent -> node calls (pull + user API). Nodes are often behind
	// a self-signed/internal CA: point NODE_CA_FILE at the bundle, or set
	// NODE_TLS_INSECURE=1 to skip verification (lab only).
	NodeCAFile      string
	NodeTLSInsecure bool
}

// PortalNode is one node the portal provisions on, with the display name users
// see on the portal card and in the client's profile title.
type PortalNode struct {
	ID   string
	Name string
}

// PortalEnabled reports whether the self-service /me portal should be mounted: it
// needs a public sub base to hand out and a node to provision users on.
func (c Config) PortalEnabled() bool {
	return c.SubBaseURL != "" && len(c.PortalNodes) > 0
}

// PortalNodeName returns the display name of a portal node ("" if the node is
// not a portal node or has no name).
func (c Config) PortalNodeName(id string) string {
	for _, n := range c.PortalNodes {
		if n.ID == id {
			return n.Name
		}
	}
	return ""
}

// ParsePortalNodes parses "id=Name,id2,id3=Name3" (Name optional), falling back
// to the legacy single id. Duplicates and blanks are dropped, order is kept.
func ParsePortalNodes(list, legacy string) []PortalNode {
	if strings.TrimSpace(list) == "" {
		list = legacy
	}
	var out []PortalNode
	seen := map[string]bool{}
	for _, item := range strings.Split(list, ",") {
		id, name, _ := strings.Cut(strings.TrimSpace(item), "=")
		id, name = strings.TrimSpace(id), strings.TrimSpace(name)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, PortalNode{ID: id, Name: name})
	}
	return out
}

// NodeHTTPClient builds the HTTP client the agent uses to reach nodes, honouring
// the configured CA bundle / insecure flag.
func (c Config) NodeHTTPClient(timeout time.Duration) *http.Client {
	tlsCfg := &tls.Config{InsecureSkipVerify: c.NodeTLSInsecure} //nolint:gosec // opt-in, lab only
	if c.NodeCAFile != "" {
		if pem, err := os.ReadFile(c.NodeCAFile); err == nil {
			pool := x509.NewCertPool()
			if pool.AppendCertsFromPEM(pem) {
				tlsCfg.RootCAs = pool
			}
		}
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
}

// FromEnv builds a Config from environment variables with sane defaults.
func FromEnv() (Config, error) {
	c := Config{
		Listen:             env("AGENT_LISTEN", "0.0.0.0:8443"),
		APIToken:           os.Getenv("AGENT_API_TOKEN"),
		PostgresDSN:        os.Getenv("POSTGRES_DSN"),
		ClickHouseAddr:     env("CLICKHOUSE_ADDR", "clickhouse:9000"),
		ClickHouseDB:       env("CLICKHOUSE_DB", "vlr"),
		ClickHouseUser:     env("CLICKHOUSE_USER", "default"),
		ClickHousePass:     os.Getenv("CLICKHOUSE_PASSWORD"),
		PullThresholdBytes: envInt64("PULL_THRESHOLD_BYTES", 256<<20),
		ReconcileEvery:     time.Duration(envInt64("RECONCILE_SECONDS", 600)) * time.Second,
		DownAfterMisses:    int(envInt64("DOWN_AFTER_MISSES", 3)),
		HeartbeatInterval:  time.Duration(envInt64("HEARTBEAT_SECONDS", 20)) * time.Second,
		NodeCAFile:         os.Getenv("NODE_CA_FILE"),
		NodeTLSInsecure:    os.Getenv("NODE_TLS_INSECURE") == "1",
		SubTitle:           os.Getenv("AGENT_SUB_TITLE"),
		SubUpdateHrs:       int(envInt64("AGENT_SUB_UPDATE_HOURS", 12)),
		SubSupportURL:      os.Getenv("AGENT_SUB_SUPPORT_URL"),

		SubBaseURL:             os.Getenv("PUBLIC_SUB_BASE"),
		PortalNodes:            ParsePortalNodes(os.Getenv("PORTAL_NODE_IDS"), os.Getenv("PORTAL_NODE_ID")),
		PortalHeader:           env("PORTAL_HEADER", "X-Authentik-Email"),
		PortalProfile:          os.Getenv("PORTAL_PROFILE"),
		AuthentikWebhookSecret: os.Getenv("AUTHENTIK_WEBHOOK_SECRET"),
	}
	if c.PostgresDSN == "" {
		return c, fmt.Errorf("POSTGRES_DSN is required")
	}
	return c, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt64(k string, def int64) int64 {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}
