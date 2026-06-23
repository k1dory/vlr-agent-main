"""Бизнес-логика: выдача и отзыв доступа через API агента. Общая для bot и worker."""
from __future__ import annotations

import logging
from datetime import datetime, timezone

from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from .agent_client import AgentClient
from .config import settings
from .models import ACTIVE, REVOKED, Subscriber

log = logging.getLogger("service")


def _now() -> datetime:
    return datetime.now(timezone.utc)


async def provision(
    session: AsyncSession, agent: AgentClient, telegram_id: int, username: str | None
) -> Subscriber:
    """Идемпотентно выдаёт доступ: создаёт юзера у агента и пишет в леджер.

    Уже active -> возвращаем (ссылка постоянна). Был revoked или нет в леджере ->
    создаём ключ у агента и (ре)активируем.
    """
    sub = await session.get(Subscriber, telegram_id)
    if sub is not None and sub.status == ACTIVE and sub.sub_path:
        if username and sub.username != username:
            sub.username = username
            await session.commit()
        return sub

    resp = await agent.create_user(
        node_id=settings.node_id,
        telegram_id=telegram_id,
        expires_in=settings.sub_expires_in,
        label=settings.sub_label,
        title=settings.sub_title,
    )
    sub_token = resp.get("sub_token", "")
    sub_path = resp.get("sub_path") or (f"/sub/{sub_token}" if sub_token else "")
    uuid = resp.get("uuid", "")
    if not sub_path:
        raise RuntimeError("agent did not return sub_path/sub_token")

    if sub is None:
        sub = Subscriber(telegram_id=telegram_id, username=username)
        session.add(sub)
    sub.username = username or sub.username
    sub.uuid = uuid
    sub.sub_token = sub_token
    sub.sub_path = sub_path
    sub.status = ACTIVE
    sub.revoked_at = None
    sub.last_checked = _now()
    await session.commit()
    log.info("provisioned tg=%s sub_path=%s", telegram_id, sub_path)
    return sub


async def revoke(session: AsyncSession, agent: AgentClient, sub: Subscriber) -> None:
    """Немедленный отзыв: удаляет юзера у агента и помечает revoked."""
    await agent.delete_user(str(sub.telegram_id))
    sub.status = REVOKED
    sub.revoked_at = _now()
    sub.last_checked = _now()
    await session.commit()
    log.info("revoked tg=%s", sub.telegram_id)


async def active_subscribers(session: AsyncSession) -> list[Subscriber]:
    res = await session.execute(select(Subscriber).where(Subscriber.status == ACTIVE))
    return list(res.scalars().all())
