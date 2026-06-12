// Command vlr-main-agent is the out-of-tree control plane for a fleet of vlr
// nodes. It ingests node heartbeats, runs the delta-triggered pull loop, and
// persists keys to PostgreSQL + traffic to ClickHouse, while exposing a central
// API to manage every user/subscription and serve base64 subscription links.
//
// Unlike the vlr node binary (zero external deps, runs the data plane), this is a
// containerized service with real database drivers. Keys never include a server
// private key — only the public Reality material needed to rebuild client links.
package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/k1dory/vlr-main-agent/internal/api"
	"github.com/k1dory/vlr-main-agent/internal/collector"
	"github.com/k1dory/vlr-main-agent/internal/config"
	"github.com/k1dory/vlr-main-agent/internal/metrics"
	"github.com/k1dory/vlr-main-agent/internal/store"
)

func main() {
	// `vlr-main-agent healthz` is the container HEALTHCHECK probe: it hits the
	// local API and exits 0/1. Works on distroless (no shell/curl needed).
	if len(os.Args) > 1 && os.Args[1] == "healthz" {
		os.Exit(healthz())
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.FromEnv()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pg, err := store.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Error("postgres", "err", err)
		os.Exit(1)
	}
	defer pg.Close()
	log.Info("postgres ready")

	var ch *metrics.CH
	if cfg.ClickHouseAddr != "" {
		ch, err = metrics.Open(ctx, cfg.ClickHouseAddr, cfg.ClickHouseDB, cfg.ClickHouseUser, cfg.ClickHousePass)
		if err != nil {
			log.Error("clickhouse", "err", err)
			os.Exit(1)
		}
		defer ch.Close()
		log.Info("clickhouse ready")
	}

	coll := collector.New(cfg, pg, ch, log)
	go coll.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.New(cfg, pg, coll, log).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sd, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sd)
	}()

	log.Info("vlr-main-agent up", "listen", cfg.Listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("api server", "err", err)
		os.Exit(1)
	}
}

// healthz probes the local /healthz endpoint. Returns 0 if healthy, 1 otherwise.
func healthz() int {
	addr := os.Getenv("AGENT_LISTEN")
	if addr == "" {
		addr = "0.0.0.0:8443"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = "8443"
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
