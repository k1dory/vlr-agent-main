"""Воркер: периодически сверяет леджер с членством в канале и ревокает
отвалившихся (страховка от пропущенных realtime chat_member-апдейтов).

Запуск: python -m app.worker
"""
from __future__ import annotations

import asyncio
import logging
from datetime import datetime, timezone

from aiogram import Bot

from ..agent_client import AgentClient
from ..config import settings
from ..db import Session, init_db
from ..membership import membership_allows
from ..service import active_subscribers, revoke

log = logging.getLogger("worker")


async def _is_subscribed(bot: Bot, user_id: int) -> bool:
    try:
        m = await bot.get_chat_member(settings.channel, user_id)
    except Exception as e:
        log.warning("get_chat_member %s: %s", user_id, e)
        return True  # при ошибке НЕ ревокаем — избегаем ложных отзывов
    return membership_allows(m.status, getattr(m, "is_member", None))


async def sweep_once(bot: Bot) -> int:
    revoked = 0
    async with Session() as session:
        subs = await active_subscribers(session)
        agent = AgentClient(settings.agent_url, settings.agent_api_token)
        try:
            for sub in subs:
                ok = await _is_subscribed(bot, sub.telegram_id)
                sub.last_checked = datetime.now(timezone.utc)
                if not ok:
                    await revoke(session, agent, sub)
                    revoked += 1
                else:
                    await session.commit()
        finally:
            await agent.aclose()
    return revoked


async def main() -> None:
    logging.basicConfig(
        level=settings.log_level,
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )
    await init_db()
    bot = Bot(settings.bot_token)
    log.info("worker up, interval=%ss", settings.sweep_interval_seconds)
    try:
        while True:
            try:
                n = await sweep_once(bot)
                log.info("sweep done, revoked=%d", n)
            except Exception:
                log.exception("sweep failed")
            await asyncio.sleep(settings.sweep_interval_seconds)
    finally:
        await bot.session.close()


if __name__ == "__main__":
    asyncio.run(main())
