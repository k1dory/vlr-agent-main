# Центральная выдача подписок через Authentik (vlr-main-agent)

Юзер открывает приложение «Личный VPN» в Authentik → попадает на портал агента →
получает **свою** ссылку с QR. Деактивация в Authentik → вебхук → доступ снят.
Всё централизованно: ключ создаётся на узле, но управляется агентом (PG + ClickHouse).

## Модель

```
ВЫДАЧА:
  сотрудник (группа vlr-main) → https://link.infrashark.tech → forward_auth (Authentik)
   → agent /me читает X-Authentik-Email → создаёт ключ на каждом узле из PORTAL_NODE_IDS (через узел); все ключи — в одной подписке, по ключу на сервер
   → пишет в Postgres → отдаёт страницу: QR + https://link.infrashark.tech/sub/<token>

ОТЗЫВ:
  Authentik: юзер деактивирован/удалён → Notification Rule → Webhook
   → POST /v1/authentik/event {email} → agent удаляет юзера на узле и в Postgres
```

Портал трогает только своих юзеров (`external_id=authentik`); ключи, заведённые
ботом/вручную, вебхук не удаляет.

## 1. Группа и приложение в Authentik

1. **Directory → Groups → Create**: `vlr-main` — кто в ней, тот видит VPN.
2. **Providers → Create → Proxy Provider**:
   - Type: **Forward auth (single application)**
   - External host: `https://link.infrashark.tech` (домен портала = `PORTAL_DOMAIN`)
3. **Applications → Create**: name `Личный VPN`, slug `vlr-main`, Provider — из п.2,
   Launch URL `https://link.infrashark.tech`. Bind policy: разрешить группе `vlr-main`.
4. **Outposts → authentik Embedded Outpost → Edit** → добавить провайдер в Applications.

> Домен портала (`link.infrashark.tech`) живёт на хосте control plane, а Authentik —
> на EDGE. Portal Caddy проксирует `/outpost.goauthentik.io/*` на `AUTHENTIK_HOST`,
> поэтому login/callback работают кросс-доменно и кука ставится на домен портала.

## 2. Webhook авто-отзыва

**Notification Webhook Mapping — Body** (Customization → Property Mappings →
Create → *Notification Webhook Mapping*, `vlr-revoke-body`):

```python
event = request.context.get("event")
model = event.context.get("model", {})
from authentik.core.models import User
user = User.objects.filter(pk=model.get("pk")).first()
return {
    "action": event.action,
    "email": user.email if user else model.get("name"),
    "is_active": bool(user.is_active) if user else False,
}
```

**Notification Webhook Mapping — Header** (`vlr-revoke-header`):

```python
return {"Authorization": "Bearer <AUTHENTIK_WEBHOOK_SECRET>"}
```

**Transport** (Events → Notification Transports → *Webhook (generic)*, `vlr-webhook`):

| Поле | Значение |
|---|---|
| Webhook URL | `https://link.infrashark.tech/v1/authentik/event` |
| Webhook Mapping (Body) | `vlr-revoke-body` |
| Webhook Mapping (Header) | `vlr-revoke-header` |

**Expression Policy** (`vlr-revoke-when`) — только на деактивацию/удаление юзера:

```python
event = request.context.get("event")
if not event or event.action not in ("model_updated", "model_deleted"):
    return False
model = event.context.get("model", {})
if model.get("app") != "authentik_core" or model.get("model_name") != "user":
    return False
if event.action == "model_deleted":
    return True
from authentik.core.models import User
user = User.objects.filter(pk=model.get("pk")).first()
return bool(user and not user.is_active)
```

**Notification Rule** (`vlr-revoke`): Transport = `vlr-webhook`, Group — пусто,
привязать политику `vlr-revoke-when`.

## 3. Что должно совпасть

| Authentik | vlr-main-agent (.env) |
|---|---|
| External host провайдера | `PORTAL_DOMAIN` = `link.infrashark.tech` |
| Bearer в Header-mapping | `AUTHENTIK_WEBHOOK_SECRET` |
| Webhook URL | `https://$PORTAL_DOMAIN/v1/authentik/event` |
| — | `PORTAL_NODE_IDS` = `id=Имя,id2=Имя2` — зарегистрированные узлы (старое `PORTAL_NODE_ID` = один узел) |
| — | `PUBLIC_SUB_BASE` = `https://$PORTAL_DOMAIN` |

## 4. Проверка

```bash
# портал без личности — 403 (агент за Caddy; локально бьём напрямую)
curl -si http://127.0.0.1:8443/me | head -1

# с личностью (эмулируем то, что ставит Caddy после Authentik) — 200 + QR + ссылка
curl -s http://127.0.0.1:8443/me -H 'X-Authentik-Email: i.ivanov@genomed.ru' | grep -o '/sub/[a-f0-9]*'

# вебхук отзыва — юзер удаляется на узле и в Postgres
curl -si -XPOST http://127.0.0.1:8443/v1/authentik/event \
  -H "Authorization: Bearer $AUTHENTIK_WEBHOOK_SECRET" \
  -d '{"action":"model_updated","email":"i.ivanov@genomed.ru","is_active":false}'
```

В браузере: член группы `vlr-main` открывает плитку → видит QR и ссылку. Деактивация
в Authentik → ключ пропадает (проверь `curl .../v1/users -H "Authorization: Bearer $AGENT_API_TOKEN"`).

## Грабли

- **Провайдер не в Embedded Outpost** → forward_auth 403 на всё.
- **Кросс-домен кука**: обязательно проксировать `/outpost.goauthentik.io/*` на
  Authentik через домен портала (уже сделано в `Caddyfile.portal`).
- **Узел из `PORTAL_NODE_IDS` не зарегистрирован или лежит** → его ключ просто не попадёт в подписку и создастся при следующем обновлении; 502 — только если не удалось ни на одном.
  Сначала `vlr node connect` (или `POST /v1/nodes`).
- **Секрет не совпал** → вебхук `401`, юзеры не отзываются. Сверь Header-mapping и
  `AUTHENTIK_WEBHOOK_SECRET`.
