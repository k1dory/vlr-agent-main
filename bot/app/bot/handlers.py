"""Хэндлеры: /start, проверка подписки, realtime-ревок при выходе из канала."""
from __future__ import annotations

import logging

from aiogram import Bot, F, Router
from aiogram.filters import CommandStart
from aiogram.types import (
    CallbackQuery,
    ChatMemberUpdated,
    InlineKeyboardButton,
    InlineKeyboardMarkup,
    Message,
)

from ..agent_client import AgentClient
from ..config import settings
from ..db import Session
from ..membership import membership_allows
from ..models import ACTIVE, Subscriber
from ..service import provision, revoke

log = logging.getLogger("bot")
router = Router()


def _subscribe_kb() -> InlineKeyboardMarkup:
    return InlineKeyboardMarkup(
        inline_keyboard=[
            [InlineKeyboardButton(text="📢 Подписаться", url=settings.channel_invite)],
            [InlineKeyboardButton(text="✅ Я подписался", callback_data="check")],
        ]
    )


async def is_subscribed(bot: Bot, user_id: int) -> bool:
    try:
        m = await bot.get_chat_member(settings.channel, user_id)
    except Exception as e:
        log.warning("get_chat_member failed for %s: %s", user_id, e)
        return False
    return membership_allows(m.status, getattr(m, "is_member", None))


async def _grant(bot: Bot, user_id: int, username: str | None) -> str | None:
    if not await is_subscribed(bot, user_id):
        return None
    async with Session() as session:
        agent = AgentClient(settings.agent_url, settings.agent_api_token)
        try:
            sub = await provision(session, agent, user_id, username)
        finally:
            await agent.aclose()
        return sub.public_link(settings.public_sub_base)


_OK_TEXT = (
    "Готово! Твоя персональная ссылка-подписка (вставь в v2rayNG / Hiddify / NekoBox):\n\n"
    "`{link}`\n\n"
    "Ключи обновляются автоматически — приложение само подтянет свежий конфиг. "
    "Останешься без подписки на канал — доступ отзовётся."
)


@router.message(CommandStart())
async def on_start(message: Message, bot: Bot) -> None:
    user = message.from_user
    if user is None:
        return
    link = await _grant(bot, user.id, user.username)
    if link is None:
        await message.answer(
            "Чтобы получить доступ к VPN — подпишись на канал и нажми «Я подписался».",
            reply_markup=_subscribe_kb(),
        )
        return
    await message.answer(_OK_TEXT.format(link=link), parse_mode="Markdown")


@router.callback_query(F.data == "check")
async def on_check(cb: CallbackQuery, bot: Bot) -> None:
    user = cb.from_user
    link = await _grant(bot, user.id, user.username)
    if link is None:
        await cb.answer("Подписка не найдена. Подпишись и попробуй снова.", show_alert=True)
        return
    if cb.message is not None:
        await cb.message.answer(_OK_TEXT.format(link=link), parse_mode="Markdown")
    await cb.answer()


@router.chat_member()
async def on_chat_member(event: ChatMemberUpdated) -> None:
    """Бот — админ канала и получает апдейты о членстве. Вышел -> мгновенный ревок."""
    if settings.channel.lstrip("-").isdigit() and str(event.chat.id) != settings.channel:
        return
    still_in = membership_allows(
        event.new_chat_member.status, getattr(event.new_chat_member, "is_member", None)
    )
    if still_in:
        return
    user_id = event.new_chat_member.user.id
    async with Session() as session:
        sub = await session.get(Subscriber, user_id)
        if sub is None or sub.status != ACTIVE:
            return
        agent = AgentClient(settings.agent_url, settings.agent_api_token)
        try:
            await revoke(session, agent, sub)
        finally:
            await agent.aclose()
    log.info("auto-revoked tg=%s on channel leave", user_id)
