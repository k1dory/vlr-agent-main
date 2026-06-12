-- vlr-main-agent PostgreSQL schema. Idempotent: applied on every startup.
-- Holds the node registry, each node's PUBLIC Reality entry snapshot, and the
-- authoritative user/key list. No server private keys are ever stored here.

CREATE TABLE IF NOT EXISTS nodes (
    node_id          TEXT PRIMARY KEY,
    -- reach / auth (how the agent pulls and calls the node's user API)
    pull_url         TEXT NOT NULL DEFAULT '',
    pull_bearer      TEXT NOT NULL DEFAULT '',
    heartbeat_token  TEXT NOT NULL DEFAULT '',
    user_api_url     TEXT NOT NULL DEFAULT '',
    user_api_token   TEXT NOT NULL DEFAULT '',
    region           TEXT NOT NULL DEFAULT '',
    -- public Reality entry snapshot (for central link rebuild)
    host             TEXT NOT NULL DEFAULT '',
    port             INT  NOT NULL DEFAULT 443,
    sni              TEXT NOT NULL DEFAULT '',
    public_key       TEXT NOT NULL DEFAULT '',
    fingerprint      TEXT NOT NULL DEFAULT 'randomized',
    short_ids        JSONB NOT NULL DEFAULT '[]',
    -- running view (drives the delta-pull decision)
    config_version   BIGINT NOT NULL DEFAULT 0,
    pulled_cfg_ver   BIGINT NOT NULL DEFAULT 0,
    heard_bytes      BIGINT NOT NULL DEFAULT 0,
    pulled_bytes     BIGINT NOT NULL DEFAULT 0,
    last_seq         BIGINT NOT NULL DEFAULT 0,
    last_seen        TIMESTAMPTZ,
    last_pull        TIMESTAMPTZ,
    healthy          BOOLEAN NOT NULL DEFAULT false,
    cascade_up       BOOLEAN NOT NULL DEFAULT false,
    down             BOOLEAN NOT NULL DEFAULT false
);

CREATE TABLE IF NOT EXISTS users (
    uuid         TEXT PRIMARY KEY,           -- VLESS id = stable identity
    node_id      TEXT NOT NULL REFERENCES nodes(node_id) ON DELETE CASCADE,
    email        TEXT NOT NULL DEFAULT '',
    telegram_id  BIGINT NOT NULL DEFAULT 0,
    external_id  TEXT NOT NULL DEFAULT '',
    profile      TEXT NOT NULL DEFAULT '',   -- 'vision' | '' (plain Reality)
    short_id     TEXT NOT NULL DEFAULT '',
    -- High-entropy token for the public /sub/<token> URL. NEVER expose keys via a
    -- guessable ref (telegram_id/email); this is the only public subscription key.
    sub_token    TEXT NOT NULL DEFAULT '',
    -- Subscription presentation / lifecycle (agent-side metadata; the node never
    -- sets these, so pulls must not overwrite them).
    label        TEXT NOT NULL DEFAULT '',   -- per-key display name -> vless:// #fragment ("For Learn 7d")
    title        TEXT NOT NULL DEFAULT '',   -- per-user Profile-Title override (else AGENT_SUB_TITLE)
    expires_at   TIMESTAMPTZ,                -- NULL = never expires; past => reaped
    enabled      BOOLEAN NOT NULL DEFAULT true,
    rx_bytes     BIGINT NOT NULL DEFAULT 0,
    tx_bytes     BIGINT NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Additive migrations for existing deployments (CREATE TABLE IF NOT EXISTS does
-- not add columns to a table that already exists). Each is idempotent.
ALTER TABLE users ADD COLUMN IF NOT EXISTS sub_token  TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS label      TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS title      TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS users_node_idx     ON users(node_id);
CREATE INDEX IF NOT EXISTS users_telegram_idx ON users(telegram_id) WHERE telegram_id <> 0;
CREATE INDEX IF NOT EXISTS users_email_idx    ON users(email)       WHERE email <> '';
CREATE UNIQUE INDEX IF NOT EXISTS users_sub_token_idx ON users(sub_token) WHERE sub_token <> '';
