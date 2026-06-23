# vlr-main-bot

Telegram subscription-gate бот для [`vlr-main-agent`](../). Гейтит доступ к VPN по
подписке на канал: подписан → бот выдаёт персональную ссылку-подписку; отписался →
ключ мгновенно отзывается.

**Сайт и БД ключей писать не нужно** — их даёт сам агент:
- ссылку `https://<домен>/sub/<sub_token>` обслуживает `GET /sub` агента (ключи
  подтягиваются из текущего конфига ноды автоматически);
- ключи хранит Postgres агента, срок enforce-ит его reaper.

Бот лишь дёргает API агента и держит **мини-леджер** (SQLite) для идемпотентного
`/start` и реконсиляции.

## Поток

```
/start ──▶ get_chat_member(канал)
            ├─ не подписан ─▶ кнопка «Подписаться»
            └─ подписан ────▶ agent POST /v1/users {node_id, telegram_id}
                              ─▶ sub_path ─▶ https://<домен>/sub/<token>
chat_member: left/kicked ──▶ agent DELETE /v1/users/<tg_id>  (мгновенно)
worker (раз в N сек) ──────▶ сверяет леджер с членством ─▶ ревок отвалившихся
```

## Компоненты

| Сервис | Команда | Роль |
|---|---|---|
| `bot` | `python -m app.bot` | `/start`, гейт, выдача ссылки, realtime-ревок |
| `worker` | `python -m app.worker` | sweep-реконсиляция членства |

Оба используют общий код (`app/`) и один SQLite-леджер (`/data/bot.db`).

## Конфиг (env)

| Переменная | Назначение |
|---|---|
| `BOT_TOKEN` | токен бота (BotFather) |
| `CHANNEL` | `@username` или `-100<id>` канала (бот должен быть **админом**) |
| `CHANNEL_INVITE` | `https://t.me/...` для кнопки |
| `AGENT_URL` | API агента (`http://agent:8443` в одном compose; `https://<домен>` если бот на другом сервере) |
| `AGENT_API_TOKEN` | `AGENT_API_TOKEN` агента |
| `NODE_ID` | id ноды, на которой агент создаёт ключ (`vlr node connect`) |
| `PUBLIC_SUB_BASE` | база публичной ссылки, напр. `https://learn.example.com` |
| `SUB_EXPIRES_IN` | срок ключа: `30d`/`12h`/пусто (без срока) |
| `SUB_LABEL` / `SUB_TITLE` | имя ключа и заголовок подписки (emoji-иконка) |
| `SWEEP_INTERVAL_SECONDS` | период реконсиляции (по умолчанию 600) |

## Важное

- **Бот должен быть админом канала** — иначе Telegram не шлёт `chat_member`-апдейты
  и realtime-ревок не сработает (останется только периодический sweep).
- На другом сервере: `AGENT_URL=https://<домен агента>`, и убери `depends_on: agent`
  из сервиса бота. API агента должно быть доступно (TLS Caddy или WG).

## Локально (без Docker)

```bash
pip install -r requirements.txt
python -m pytest tests/         # юнит-тесты логики членства
DATABASE_URL=sqlite+aiosqlite:///bot.db python -m app.bot
```
