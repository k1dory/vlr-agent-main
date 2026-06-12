# vlr-main-agent — control plane for a vlr node fleet

`vlr-main-agent` is the **out-of-tree** main server for [`vlr`](https://github.com/k1dory/vlr)
VPN nodes. The `vlr` binary is deliberately dependency-free and runs the data
plane; this agent is the opposite end — a containerized service with real
databases that **collects keys and metrics from every node** and **manages all
users and subscriptions centrally**.

| | `vlr` node | `vlr-main-agent` (this repo) |
|---|---|---|
| Role | terminates Reality, runs RU→EU cascade | aggregates, persists, issues subs |
| Deps | **zero** external Go deps (offline build) | pgx + clickhouse-go, Docker |
| State | local JSON | **PostgreSQL** (keys) + **ClickHouse** (metrics) |
| Runs on | systemd / host (needs kernel WireGuard) | Docker / compose |

It speaks the node's existing heartbeat + pull protocol over HTTP/JSON — no shared
Go module, the JSON is the contract (`internal/wire`).

## What it does

- **Ingests heartbeats** (`POST /v1/heartbeat`) from nodes → liveness + summary
  into PostgreSQL, a health sample into ClickHouse.
- **Delta-triggered pull** (`internal/collector`): when a node's heard state
  drifts from its pulled state (config bumped, traffic over threshold, recovered,
  or reconcile due), it `GET`s the node's `/v1/pull`, then persists every user/key
  to PostgreSQL and a per-user traffic sample to ClickHouse. Same pure decision
  logic as vlr (`wire.ShouldPull`), unit-tested.
- **Central user management**: create/delete users on any node through one API.
  Create is proxied to the owning node (which mutates its Xray config), then the
  node is resynced so PostgreSQL/ClickHouse reflect the new key immediately.
- **Central subscriptions**: rebuilds the exact `vless://` link the node issued
  (from the public Reality entry snapshot + per-user short id) and serves the
  base64 subscription at `GET /sub/<token>` — keyed by a **per-user 128-bit
  sub_token** (never a guessable email/telegram id), so the URL can't be
  enumerated to harvest other users' keys.
- A **Telegram subscription-gate bot** is a separate component; compose has a
  commented placeholder pointed at this agent's API.

## Quick start

```bash
git clone git@github.com:k1dory/vlr-main-agent.git
cd vlr-main-agent
chmod +x install.sh && ./install.sh     # installs Docker if missing, generates
                                         # secrets, brings up agent+PG+ClickHouse
```

`install.sh` writes `deploy/compose/.env` with generated `AGENT_API_TOKEN` /
DB passwords and starts the stack. Data lives in **`./data/`** (bind mounts) — it
survives `docker compose down`; only `down` + deleting `./data` wipes it.

Manual compose:

```bash
cp deploy/compose/.env.example deploy/compose/.env && nano deploy/compose/.env
make up        # docker compose up -d --build
make logs      # follow the agent
make ps
```

## Register a node, then manage users

**Easiest:** run `vlr node connect` *on the node* — it switches the node to the
`child` role, generates the tokens, and registers it here for you (pass
`--agent-token <AGENT_API_TOKEN>`). The manual equivalent from the main side:

```bash
TOKEN=$(grep ^AGENT_API_TOKEN= deploy/compose/.env | cut -d= -f2)

# 1. register a node (pull_bearer = node's child.pull_bearer; user_api_token = its api_token)
curl -XPOST http://localhost:8443/v1/nodes -H "Authorization: Bearer $TOKEN" -d '{
  "node_id":"ru-yc-msk-01",
  "pull_url":"https://NODE:9777/v1/pull",  "pull_bearer":"<node pull_bearer>",
  "user_api_url":"https://NODE:9777",      "user_api_token":"<node api_token>",
  "heartbeat_token":"<node child.token>",  "region":"RU/Yandex"
}'

# 2. create a user on that node (proxied to the node, then resynced centrally)
curl -XPOST http://localhost:8443/v1/users -H "Authorization: Bearer $TOKEN" \
  -d '{"node_id":"ru-yc-msk-01","telegram_id":9876567,"profile":"vision"}'
# -> { "uuid":"...", "link":"vless://...", "subscription":"<base64>",
#      "sub_token":"<32 hex>", "sub_path":"/sub/<32 hex>" }

# 3. list / delete  (delete by uuid|email|id|telegram-id — admin, token-guarded)
curl http://localhost:8443/v1/users -H "Authorization: Bearer $TOKEN"
curl -XDELETE http://localhost:8443/v1/users/9876567 -H "Authorization: Bearer $TOKEN"

# 4. subscription URL the client app polls — use the sub_token from step 2
#    (NOT telegram_id/email; those are not accepted on /sub). Front with TLS.
curl http://localhost:8443/sub/<sub_token>
```

Point the node's heartbeat at the agent: set the node's `child.main_url` to
`https://AGENT:8443/v1` and `vlr serve` on the node will push heartbeats here.

## API

| Method | Path | Auth | Purpose |
|---|---|---|---|
| `POST` | `/v1/heartbeat` | node heartbeat token | node → agent liveness/summary |
| `GET` | `/v1/nodes` | `AGENT_API_TOKEN` | list nodes + their view |
| `POST` | `/v1/nodes` | `AGENT_API_TOKEN` | register/update a node |
| `GET` | `/v1/users` | `AGENT_API_TOKEN` | list users (`?node_id=` filter) |
| `POST` | `/v1/users` | `AGENT_API_TOKEN` | create on a node (returns link+sub) |
| `DELETE` | `/v1/users/<ref>` | `AGENT_API_TOKEN` | delete on the owning node (`ref`=uuid/email/id/tg-id) |
| `GET` | `/sub/<token>` | — | base64 subscription (`token`=per-user sub_token, or uuid) |
| `GET` | `/healthz` | — | liveness |

> Put `:8443` behind TLS in production — either the bundled Caddy terminator
> (`docker compose --profile tls up -d`, set `AGENT_DOMAIN`) or your own proxy.
> `/sub/<token>` is the only unauthenticated path and it accepts **only** the
> high-entropy sub_token (or the uuid) — never a guessable email/telegram id.

### TLS to the nodes

The agent calls each node's pull + user API over HTTPS. If nodes use a
self-signed or internal CA, set `NODE_CA_FILE` to the bundle (mounted into the
container) or `NODE_TLS_INSECURE=1` for a lab (skips verification).

## Storage

- **PostgreSQL** — `nodes` (registry + public entry snapshot + running view) and
  `users` (keys/identities). Schema auto-applied on startup (`internal/store/schema.sql`).
  No server private keys are ever stored.
- **ClickHouse** — `vlr.traffic` (per-user rx/tx time series) and `vlr.node_health`
  (heartbeat samples). MergeTree, partitioned by month.

Both run as containers with bind-mounted data under `./data/`. The `users` table
also holds each user's `sub_token` (set once, stable across pulls).

> **Postgres volume permissions:** the official image runs as uid 999. If you
> pre-create `./data/postgres` as another owner, initdb fails — let the container
> create it, or `chown -R 999:999 ./data/postgres`.

## Production notes (remaining hardening)

The control plane works end to end, but before a large production fleet:

- **Versioned migrations** — the schema is applied as `CREATE IF NOT EXISTS`
  (good for first boot, can't evolve columns). Adopt a migration tool / version
  table before changing existing columns.
- **Observability** — no `/metrics` (Prometheus) or access/audit log yet.
- **Rate limiting** — none on the management or `/sub` endpoints.
- **Async ClickHouse** — health/traffic inserts are synchronous per heartbeat/
  pull; buffer them for many nodes.
- **Secret handling** — DB passwords ride in the agent env (visible to
  `docker inspect`); consider Docker/host secrets.
- **In-tree node `role: main`** (the zero-dep aggregator in `vlr`) does **not**
  verify heartbeat tokens — use this agent for anything multi-tenant.

## Build / test (without Docker)

```bash
make build   # CGO_ENABLED=0 static binary
make test    # unit tests (delta-pull logic)
make vet
```

Configuration is 12-factor (env); see `internal/config` and `deploy/compose/.env.example`.

## Layout

```
cmd/vlr-main-agent/   entrypoint (serve)
internal/wire/        node JSON DTOs + pure ShouldPull/MarkDown (tested)
internal/config/      env config
internal/store/       PostgreSQL (nodes, users/keys) + embedded schema
internal/metrics/     ClickHouse (traffic, node_health)
internal/collector/   heartbeat ingest + delta-pull loop
internal/sub/         vless:// + base64 rebuild (matches vlr byte-for-byte)
internal/api/         HTTP API (heartbeat, nodes, users, /sub)
deploy/compose/       docker-compose stack + .env.example
Dockerfile            multistage, distroless nonroot
```
