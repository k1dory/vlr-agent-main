#!/usr/bin/env bash
# vlr-main-agent installer. Brings up the control-plane stack (agent + PostgreSQL
# + ClickHouse) in Docker. Installs Docker Engine + the compose plugin if missing
# (the only dependencies — everything else runs in containers). Idempotent.
set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_DIR="$SRC_DIR/deploy/compose"
ENV_FILE="$COMPOSE_DIR/.env"

need_root() { [ "$(id -u)" -eq 0 ] || SUDO="sudo"; SUDO="${SUDO:-}"; }
need_root

# --- 1. dependency: Docker Engine + compose plugin -------------------------
ensure_docker() {
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    echo "==> Docker + compose уже есть: $(docker --version)"
    return 0
  fi
  if [ "${VLR_AUTO_DOCKER:-1}" != "1" ]; then
    echo "Docker не найден. Поставь Docker Engine + plugin docker-compose и перезапусти."
    echo "  https://docs.docker.com/engine/install/"
    exit 1
  fi
  echo "==> ставлю Docker Engine + compose-плагин (get.docker.com)"
  curl -fsSL https://get.docker.com -o /tmp/get-docker.sh
  $SUDO sh /tmp/get-docker.sh
  $SUDO systemctl enable --now docker 2>/dev/null || true
  docker compose version >/dev/null 2>&1 || {
    echo "compose-плагин не установился — поставь docker-compose-plugin вручную"; exit 1; }
}
ensure_docker

# --- 2. data dirs (predictable, persistent bind mounts) --------------------
echo "==> создаю каталоги данных под $SRC_DIR/data (переживают 'compose down')"
mkdir -p "$SRC_DIR/data/postgres" "$SRC_DIR/data/clickhouse" "$SRC_DIR/data/clickhouse-logs"

# --- 3. .env (generate secrets on first run) -------------------------------
if [ ! -f "$ENV_FILE" ]; then
  echo "==> создаю $ENV_FILE с авто-сгенерированными секретами"
  cp "$COMPOSE_DIR/.env.example" "$ENV_FILE"
  gen() { openssl rand -hex "$1" 2>/dev/null || head -c "$1" /dev/urandom | xxd -p | tr -d '\n'; }
  api_tok="$(gen 24)"; pg_pw="$(gen 16)"; ch_pw="$(gen 16)"
  # portable in-place sed (GNU/BSD)
  sed -i.bak \
    -e "s|^AGENT_API_TOKEN=.*|AGENT_API_TOKEN=${api_tok}|" \
    -e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=${pg_pw}|" \
    -e "s|^CLICKHOUSE_PASSWORD=.*|CLICKHOUSE_PASSWORD=${ch_pw}|" "$ENV_FILE"
  rm -f "$ENV_FILE.bak"
  echo "    AGENT_API_TOKEN=${api_tok}"
  echo "    (сохрани токен — им защищён управляющий API)"
else
  echo "==> $ENV_FILE уже есть, не трогаю"
fi

# --- 4. bring the stack up -------------------------------------------------
echo "==> поднимаю стек (build + up)"
cd "$SRC_DIR"
docker compose -f deploy/compose/docker-compose.yml up -d --build

echo
echo "Готово. Стек запущен:"
docker compose -f deploy/compose/docker-compose.yml ps
echo
echo "Дальше — зарегистрировать ноды и выпускать юзеров (см. README):"
echo "  TOKEN=\$(grep ^AGENT_API_TOKEN= $ENV_FILE | cut -d= -f2)"
echo "  curl -XPOST http://localhost:8443/v1/nodes -H \"Authorization: Bearer \$TOKEN\" \\"
echo "    -d '{\"node_id\":\"ru-yc-msk-01\",\"pull_url\":\"https://NODE:9777/v1/pull\",\"pull_bearer\":\"...\",\"user_api_url\":\"https://NODE:9777\",\"user_api_token\":\"...\"}'"
