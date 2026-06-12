// Package metrics is the agent's ClickHouse sink for time-series traffic and
// node-health data. PostgreSQL holds the authoritative current state (keys,
// users); ClickHouse holds the high-volume append-only history that powers
// dashboards and per-user usage over time.
package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/k1dory/vlr-main-agent/internal/wire"
)

// CH wraps a ClickHouse connection.
type CH struct {
	conn driver.Conn
	db   string
}

// Open connects to ClickHouse (native protocol) and applies the schema.
func Open(ctx context.Context, addr, db, user, pass string) (*CH, error) {
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{addr},
		Auth: clickhouse.Auth{Database: "default", Username: user, Password: pass},
		// Keep the dial resilient to compose start order.
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("clickhouse open: %w", err)
	}
	var pingErr error
	for i := 0; i < 30; i++ {
		if pingErr = conn.Ping(ctx); pingErr == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if pingErr != nil {
		return nil, fmt.Errorf("clickhouse ping: %w", pingErr)
	}
	ch := &CH{conn: conn, db: db}
	if err := ch.migrate(ctx); err != nil {
		return nil, err
	}
	return ch, nil
}

// Close shuts the connection.
func (c *CH) Close() error { return c.conn.Close() }

func (c *CH) migrate(ctx context.Context) error {
	stmts := []string{
		fmt.Sprintf(`CREATE DATABASE IF NOT EXISTS %s`, c.db),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.traffic (
			ts          DateTime DEFAULT now(),
			node_id     LowCardinality(String),
			uuid        String,
			email       String,
			profile     LowCardinality(String),
			rx_bytes    UInt64,
			tx_bytes    UInt64
		) ENGINE = MergeTree
		PARTITION BY toYYYYMM(ts)
		ORDER BY (node_id, uuid, ts)`, c.db),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.node_health (
			ts           DateTime DEFAULT now(),
			node_id      LowCardinality(String),
			healthy      UInt8,
			cascade_up   UInt8,
			user_count   UInt32,
			total_bytes  UInt64
		) ENGINE = MergeTree
		PARTITION BY toYYYYMM(ts)
		ORDER BY (node_id, ts)`, c.db),
	}
	for _, s := range stmts {
		if err := c.conn.Exec(ctx, s); err != nil {
			return fmt.Errorf("clickhouse migrate: %w", err)
		}
	}
	return nil
}

// WriteTraffic appends one per-user usage sample per user from a pull.
func (c *CH) WriteTraffic(ctx context.Context, nodeID string, users []wire.UserDetail) error {
	if len(users) == 0 {
		return nil
	}
	batch, err := c.conn.PrepareBatch(ctx, fmt.Sprintf(
		"INSERT INTO %s.traffic (node_id, uuid, email, profile, rx_bytes, tx_bytes)", c.db))
	if err != nil {
		return err
	}
	for _, u := range users {
		if err := batch.Append(nodeID, u.UUID, u.Email, u.Profile, uint64(u.RxBytes), uint64(u.TxBytes)); err != nil {
			return err
		}
	}
	return batch.Send()
}

// WriteHealth appends a node-health sample from a heartbeat.
func (c *CH) WriteHealth(ctx context.Context, hb wire.Heartbeat) error {
	return c.conn.Exec(ctx, fmt.Sprintf(
		`INSERT INTO %s.node_health (node_id, healthy, cascade_up, user_count, total_bytes)
		 VALUES (?, ?, ?, ?, ?)`, c.db),
		hb.NodeID, b2u8(hb.Healthy), b2u8(hb.CascadeUp), uint32(hb.UserCount), uint64(hb.TotalBytes))
}

func b2u8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}
