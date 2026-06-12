// Package collector is the agent's data-collection engine. It ingests node
// heartbeats and runs the delta-triggered pull loop: when a node's heard state
// has drifted from its pulled state (or a reconcile is due), it GETs the node's
// /v1/pull, then persists keys to PostgreSQL and traffic to ClickHouse.
package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/k1dory/vlr-main-agent/internal/config"
	"github.com/k1dory/vlr-main-agent/internal/metrics"
	"github.com/k1dory/vlr-main-agent/internal/store"
	"github.com/k1dory/vlr-main-agent/internal/wire"
)

// Collector wires the persistence sinks and the pull HTTP client.
type Collector struct {
	cfg   config.Config
	pg    *store.Store
	ch    *metrics.CH
	log   *slog.Logger
	httpc *http.Client
}

// New builds a Collector.
func New(cfg config.Config, pg *store.Store, ch *metrics.CH, log *slog.Logger) *Collector {
	return &Collector{
		cfg: cfg, pg: pg, ch: ch, log: log,
		httpc: cfg.NodeHTTPClient(15 * time.Second),
	}
}

// Ingest records a heartbeat: liveness/summary into PostgreSQL, a health sample
// into ClickHouse. The pull loop reacts to the updated view on its next tick.
func (c *Collector) Ingest(ctx context.Context, hb wire.Heartbeat) error {
	if err := c.pg.ApplyHeartbeat(ctx, hb); err != nil {
		return err
	}
	if c.ch != nil {
		if err := c.ch.WriteHealth(ctx, hb); err != nil {
			c.log.Warn("clickhouse health write failed", "node", hb.NodeID, "err", err)
		}
	}
	return nil
}

// Run is the scheduler loop: down-detection + delta-pull, every 5s.
func (c *Collector) Run(ctx context.Context) {
	th := wire.Thresholds{
		ByteDelta:      c.cfg.PullThresholdBytes,
		ReconcileEvery: c.cfg.ReconcileEvery,
	}
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.tick(ctx, th)
		}
	}
}

func (c *Collector) tick(ctx context.Context, th wire.Thresholds) {
	views, err := c.pg.Views(ctx)
	if err != nil {
		c.log.Warn("load node views failed", "err", err)
		return
	}
	now := time.Now()
	for _, v := range views {
		if wire.MarkDown(v.LastSeen, now, c.cfg.HeartbeatInterval, c.cfg.DownAfterMisses) {
			if !v.Down {
				c.log.Warn("node DOWN", "node", v.NodeID, "last_seen", v.LastSeen)
				_ = c.pg.MarkDown(ctx, v.NodeID)
			}
			continue
		}
		if d := wire.ShouldPull(v, now, th); d.Pull {
			c.log.Info("pull scheduled", "node", v.NodeID, "reason", d.Reason)
			if err := c.pull(ctx, v.NodeID); err != nil {
				c.log.Warn("pull failed", "node", v.NodeID, "err", err)
			}
		}
	}
	c.reapExpired(ctx)
}

// reapExpired removes users past their expires_at: it deletes them on the owning
// node (so the key stops working) and from PostgreSQL. This is what actually
// enforces the expiry the subscription advertises.
func (c *Collector) reapExpired(ctx context.Context) {
	expired, err := c.pg.ExpiredUsers(ctx)
	if err != nil {
		c.log.Warn("list expired users failed", "err", err)
		return
	}
	for _, e := range expired {
		reg, ok, rerr := c.pg.Reg(ctx, e.NodeID)
		if rerr == nil && ok && reg.UserAPIURL != "" {
			if derr := c.deleteOnNode(ctx, reg, e.UUID); derr != nil {
				c.log.Warn("reap: node delete failed, will retry", "uuid", e.UUID, "err", derr)
				continue // leave it; retry next tick rather than orphan a live key
			}
		}
		if derr := c.pg.DeleteUser(ctx, e.UUID); derr != nil {
			c.log.Warn("reap: pg delete failed", "uuid", e.UUID, "err", derr)
			continue
		}
		c.log.Info("reaped expired user", "uuid", e.UUID, "node", e.NodeID)
	}
}

// deleteOnNode calls the node's DELETE /v1/users/<uuid>.
func (c *Collector) deleteOnNode(ctx context.Context, reg store.NodeReg, uuid string) error {
	url := strings.TrimRight(reg.UserAPIURL, "/") + "/v1/users/" + uuid
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+reg.UserAPIToken)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("node delete %s: status %d", uuid, resp.StatusCode)
	}
	return nil
}

// pull fetches a node's heavy detail and commits it as authoritative.
func (c *Collector) pull(ctx context.Context, nodeID string) error {
	reg, ok, err := c.pg.Reg(ctx, nodeID)
	if err != nil || !ok || reg.PullURL == "" {
		return fmt.Errorf("no pull registration for %s: %w", nodeID, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reg.PullURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+reg.PullBearer)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pull %s: status %d", nodeID, resp.StatusCode)
	}
	var pr wire.PullResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return fmt.Errorf("decode pull %s: %w", nodeID, err)
	}
	if pr.NodeID == "" {
		pr.NodeID = nodeID
	}

	if err := c.pg.CommitPull(ctx, pr); err != nil {
		return fmt.Errorf("commit pull %s: %w", nodeID, err)
	}
	if c.ch != nil {
		if err := c.ch.WriteTraffic(ctx, pr.NodeID, pr.Users); err != nil {
			c.log.Warn("clickhouse traffic write failed", "node", nodeID, "err", err)
		}
	}
	c.log.Info("pull committed", "node", nodeID, "users", len(pr.Users), "cfgv", pr.ConfigVersion)
	return nil
}

// Pull triggers an immediate pull of one node (used right after creating/deleting
// a user so the central state reflects the change without waiting for the tick).
func (c *Collector) Pull(ctx context.Context, nodeID string) error {
	return c.pull(ctx, nodeID)
}
