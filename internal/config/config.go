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
