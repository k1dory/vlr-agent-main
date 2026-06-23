"""Точка входа бота: python -m app.bot"""
from __future__ import annotations

import asyncio
import logging

from aiogram import Bot, Dispatcher

from ..config import settings
from ..db import init_db
from .handlers import router


async def main() -> None:
    logging.basicConfig(
        level=settings.log_level,
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )
    await init_db()

    bot = Bot(settings.bot_token)
    dp = Dispatcher()
    dp.include_router(router)

    # allowed_updates обязан включать chat_member, иначе апдейты о выходе не придут.
    used = dp.resolve_used_update_types()
    logging.getLogger("bot").info("polling, updates=%s", used)
    try:
        await dp.start_polling(bot, allowed_updates=used)
    finally:
        await bot.session.close()


if __name__ == "__main__":
    asyncio.run(main())
