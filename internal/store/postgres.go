// Package store is the agent's authoritative PostgreSQL persistence: the node
// registry, every node's public Reality entry snapshot, and every user/key. It
// is what the in-tree vlr `role: main` keeps only in memory.
package store

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/k1dory/vlr-main-agent/internal/wire"
)

//go:embed schema.sql
var schemaSQL string

// genToken returns a 32-hex-char (128-bit) random token for the public /sub URL.
func genToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// Store wraps a pgx connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to PostgreSQL and applies the (idempotent) schema.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pg connect: %w", err)
	}
	// Retry the first ping briefly — in compose the agent may start before PG is
	// accepting connections even with depends_on healthcheck slack.
	var pingErr error
	for i := 0; i < 30; i++ {
		if pingErr = pool.Ping(ctx); pingErr == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if pingErr != nil {
		return nil, fmt.Errorf("pg ping: %w", pingErr)
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return nil, fmt.Errorf("pg migrate: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// --- node registry ---------------------------------------------------------

// NodeReg is how the agent reaches a node to pull from it and call its user API.
type NodeReg struct {
	NodeID         string `json:"node_id"`
	PullURL        string `json:"pull_url"`        // https://node:9777/v1/pull
	PullBearer     string `json:"pull_bearer"`     // bearer the node requires to pull
	HeartbeatToken string `json:"heartbeat_token"` // bearer the node sends on heartbeat
	UserAPIURL     string `json:"user_api_url"`    // https://node:9777  (base for /v1/users)
	UserAPIToken   string `json:"user_api_token"`  // node's api_token
	Region         string `json:"region"`
}

// RegisterNode upserts a node's reach/auth info.
func (s *Store) RegisterNode(ctx context.Context, r NodeReg) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO nodes (node_id, pull_url, pull_bearer, heartbeat_token, user_api_url, user_api_token, region)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (node_id) DO UPDATE SET
		  pull_url=EXCLUDED.pull_url, pull_bearer=EXCLUDED.pull_bearer,
		  heartbeat_token=EXCLUDED.heartbeat_token, user_api_url=EXCLUDED.user_api_url,
		  user_api_token=EXCLUDED.user_api_token, region=EXCLUDED.region`,
		r.NodeID, r.PullURL, r.PullBearer, r.HeartbeatToken, r.UserAPIURL, r.UserAPIToken, r.Region)
	return err
}

// Reg fetches a node's reach/auth info.
func (s *Store) Reg(ctx context.Context, nodeID string) (NodeReg, bool, error) {
	var r NodeReg
	err := s.pool.QueryRow(ctx, `
		SELECT node_id, pull_url, pull_bearer, heartbeat_token, user_api_url, user_api_token, region
		FROM nodes WHERE node_id=$1`, nodeID).
		Scan(&r.NodeID, &r.PullURL, &r.PullBearer, &r.HeartbeatToken, &r.UserAPIURL, &r.UserAPIToken, &r.Region)
	if err == pgx.ErrNoRows {
		return r, false, nil
	}
	return r, err == nil, err
}

// Views returns the scheduling view of every node (for the pull loop).
func (s *Store) Views(ctx context.Context) ([]wire.NodeView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT node_id, last_seen, last_pull,
		       healthy, cascade_up, down, config_version, pulled_cfg_ver, heard_bytes, pulled_bytes
		FROM nodes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []wire.NodeView
	for rows.Next() {
		var v wire.NodeView
		var lastSeen, lastPull *time.Time // NULL -> nil -> Go zero time
		if err := rows.Scan(&v.NodeID, &lastSeen, &lastPull, &v.Healthy, &v.CascadeUp,
			&v.Down, &v.HeardConfigVersion, &v.PulledConfigVersion, &v.HeardTotalBytes, &v.PulledTotalBytes); err != nil {
			return nil, err
		}
		if lastSeen != nil {
			v.LastSeen = *lastSeen
		}
		if lastPull != nil {
			v.LastPull = *lastPull
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListNodes returns every node's public row (for the operator API; secrets omitted).
func (s *Store) ListNodes(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT node_id, region, host, port, sni, config_version,
		       last_seen, last_pull, healthy, cascade_up, down
		FROM nodes ORDER BY node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, region, host, sni string
		var port int
		var cfgv int64
		var lastSeen, lastPull *time.Time
		var healthy, cascade, down bool
		if err := rows.Scan(&id, &region, &host, &port, &sni, &cfgv, &lastSeen, &lastPull, &healthy, &cascade, &down); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"node_id": id, "region": region, "host": host, "port": port, "sni": sni,
			"config_version": cfgv, "last_seen": lastSeen, "last_pull": lastPull,
			"healthy": healthy, "cascade_up": cascade, "down": down,
		})
	}
	return out, rows.Err()
}

// --- heartbeat / pull persistence ------------------------------------------

// ApplyHeartbeat records the heard summary + liveness for a node.
func (s *Store) ApplyHeartbeat(ctx context.Context, hb wire.Heartbeat) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE nodes SET last_seen=now(), last_seq=$2, healthy=$3, cascade_up=$4,
		       down=false, config_version=$5, heard_bytes=$6
		WHERE node_id=$1`,
		hb.NodeID, int64(hb.Seq), hb.Healthy, hb.CascadeUp, hb.ConfigVersion, hb.TotalBytes)
	return err
}

// MarkDown flags a node DOWN.
func (s *Store) MarkDown(ctx context.Context, nodeID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE nodes SET down=true WHERE node_id=$1`, nodeID)
	return err
}

// CommitPull persists a full pull: the entry snapshot, every user/key, and the
// pulled-state markers, in one transaction.
func (s *Store) CommitPull(ctx context.Context, pr wire.PullResponse) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	shortIDs, _ := json.Marshal(pr.Entry.ShortIDs)
	if _, err := tx.Exec(ctx, `
		UPDATE nodes SET host=$2, port=$3, sni=$4, public_key=$5, fingerprint=$6,
		       short_ids=$7, config_version=$8, pulled_cfg_ver=$8,
		       pulled_bytes=$9, last_pull=now()
		WHERE node_id=$1`,
		pr.NodeID, pr.Entry.Host, pr.Entry.Port, pr.Entry.SNI, pr.Entry.PublicKey,
		pr.Entry.Fingerprint, shortIDs, pr.ConfigVersion, totalBytes(pr.Users)); err != nil {
		return fmt.Errorf("upsert node entry: %w", err)
	}

	seen := make([]string, 0, len(pr.Users))
	for _, u := range pr.Users {
		seen = append(seen, u.UUID)
		// sub_token is set only on first insert (DO UPDATE leaves it untouched), so
		// a user's public /sub URL is stable across pulls.
		if _, err := tx.Exec(ctx, `
			INSERT INTO users (uuid, node_id, email, telegram_id, external_id, profile, short_id, sub_token, enabled, rx_bytes, tx_bytes, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, now())
			ON CONFLICT (uuid) DO UPDATE SET
			  node_id=EXCLUDED.node_id, email=EXCLUDED.email, telegram_id=EXCLUDED.telegram_id,
			  external_id=EXCLUDED.external_id, profile=EXCLUDED.profile, short_id=EXCLUDED.short_id,
			  enabled=EXCLUDED.enabled, rx_bytes=EXCLUDED.rx_bytes, tx_bytes=EXCLUDED.tx_bytes,
			  updated_at=now()`,
			u.UUID, pr.NodeID, u.Email, u.TelegramID, u.ExternalID, u.Profile, u.ShortID,
			genToken(), u.Enabled, u.RxBytes, u.TxBytes); err != nil {
			return fmt.Errorf("upsert user %s: %w", u.UUID, err)
		}
	}
	// Drop users this node no longer reports (deleted on the node side).
	if _, err := tx.Exec(ctx,
		`DELETE FROM users WHERE node_id=$1 AND NOT (uuid = ANY($2))`, pr.NodeID, seen); err != nil {
		return fmt.Errorf("prune users: %w", err)
	}
	return tx.Commit(ctx)
}

func totalBytes(us []wire.UserDetail) int64 {
	var t int64
	for _, u := range us {
		t += u.RxBytes + u.TxBytes
	}
	return t
}

// --- user lookups (for central subscription) -------------------------------

// UserRow is a stored user joined with its node's entry snapshot.
type UserRow struct {
	UUID       string
	NodeID     string
	Email      string
	TelegramID int64
	ExternalID string
	Profile    string
	ShortID    string
	SubToken   string
	Enabled    bool
	RxBytes    int64
	TxBytes    int64
	Entry      wire.EntrySnapshot
	CreatedAt  time.Time
}

const userSelectCols = `u.uuid, u.node_id, u.email, u.telegram_id, u.external_id, u.profile,
		       u.short_id, u.sub_token, u.enabled, u.rx_bytes, u.tx_bytes, u.created_at,
		       n.host, n.port, n.sni, n.public_key, n.fingerprint, COALESCE(n.short_ids,'[]')`

func scanUserRow(row pgx.Row) (UserRow, error) {
	var u UserRow
	var shortIDs []byte
	err := row.Scan(&u.UUID, &u.NodeID, &u.Email, &u.TelegramID, &u.ExternalID, &u.Profile,
		&u.ShortID, &u.SubToken, &u.Enabled, &u.RxBytes, &u.TxBytes, &u.CreatedAt,
		&u.Entry.Host, &u.Entry.Port, &u.Entry.SNI, &u.Entry.PublicKey, &u.Entry.Fingerprint, &shortIDs)
	if err == nil {
		_ = json.Unmarshal(shortIDs, &u.Entry.ShortIDs)
	}
	return u, err
}

// FindUser resolves a user by uuid | email | external_id | telegram_id and joins
// the owning node's entry snapshot. ADMIN use only (DELETE, lookups behind the
// management token) — the public /sub path must use SubUser, never this, because
// telegram_id/email are guessable.
func (s *Store) FindUser(ctx context.Context, ref string) (UserRow, bool, error) {
	u, err := scanUserRow(s.pool.QueryRow(ctx, `
		SELECT `+userSelectCols+`
		FROM users u JOIN nodes n ON n.node_id=u.node_id
		WHERE u.uuid=$1 OR (u.email<>'' AND u.email=$1)
		   OR (u.external_id<>'' AND u.external_id=$1)
		   OR (u.telegram_id<>0 AND u.telegram_id::text=$1)
		LIMIT 1`, ref))
	if err == pgx.ErrNoRows {
		return u, false, nil
	}
	return u, err == nil, err
}

// SubUser resolves a user for the PUBLIC /sub URL by its high-entropy sub_token
// only (never by a guessable email/telegram id). uuid is also accepted since it
// is itself a 128-bit secret.
func (s *Store) SubUser(ctx context.Context, token string) (UserRow, bool, error) {
	u, err := scanUserRow(s.pool.QueryRow(ctx, `
		SELECT `+userSelectCols+`
		FROM users u JOIN nodes n ON n.node_id=u.node_id
		WHERE (u.sub_token<>'' AND u.sub_token=$1) OR u.uuid=$1
		LIMIT 1`, token))
	if err == pgx.ErrNoRows {
		return u, false, nil
	}
	return u, err == nil, err
}

// ListUsers returns all users (optionally filtered by node), newest first.
func (s *Store) ListUsers(ctx context.Context, nodeID string) ([]UserRow, error) {
	q := `SELECT uuid, node_id, email, telegram_id, external_id, profile, short_id,
	             sub_token, enabled, rx_bytes, tx_bytes, created_at FROM users`
	args := []any{}
	if nodeID != "" {
		q += ` WHERE node_id=$1`
		args = append(args, nodeID)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserRow
	for rows.Next() {
		var u UserRow
		if err := rows.Scan(&u.UUID, &u.NodeID, &u.Email, &u.TelegramID, &u.ExternalID,
			&u.Profile, &u.ShortID, &u.SubToken, &u.Enabled, &u.RxBytes, &u.TxBytes, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteUser removes a user row by uuid (after the node confirms deletion).
func (s *Store) DeleteUser(ctx context.Context, uuid string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM users WHERE uuid=$1`, uuid)
	return err
}

// InsertUser stores a user the agent just created on a node, so it is queryable
// (and its /sub URL works) immediately, before the next pull syncs full detail.
// Returns the user's sub_token (existing one is kept on conflict).
func (s *Store) InsertUser(ctx context.Context, u UserRow) (string, error) {
	var subToken string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users (uuid, node_id, email, telegram_id, external_id, profile, short_id, sub_token, enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,true)
		ON CONFLICT (uuid) DO UPDATE SET node_id=EXCLUDED.node_id
		RETURNING sub_token`,
		u.UUID, u.NodeID, u.Email, u.TelegramID, u.ExternalID, u.Profile, u.ShortID, genToken()).
		Scan(&subToken)
	return subToken, err
}
