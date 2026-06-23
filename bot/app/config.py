"""Настройки бота из окружения (.env)."""
from __future__ import annotations

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", extra="ignore")

    # Telegram
    bot_token: str
    channel: str            # @username или -100<id> для get_chat_member
    channel_invite: str     # https://t.me/... — кнопка «Подписаться»

    # vlr-main-agent API
    agent_url: str = "http://agent:8443"   # внутри compose; для другого сервера — https://<домен>
    agent_api_token: str
    node_id: str            # на какой ноде агент создаёт юзера (обязателен для POST /v1/users)

    # Выдача ссылки и срок ключа
    public_sub_base: str = "https://learn.example.com"  # к нему клеится sub_path -> публичная ссылка
    sub_expires_in: str = ""    # "30d"/"12h"/"" (без срока); агент-reaper удалит по истечении
    sub_label: str = ""         # имя ключа (#fragment), напр. "Канал"
    sub_title: str = ""         # перебивает AGENT_SUB_TITLE для этого юзера (emoji-иконка)

    # Локальный леджер (SQLite, без второго Postgres)
    database_url: str = "sqlite+aiosqlite:////data/bot.db"

    # Воркер
    sweep_interval_seconds: int = 600

    log_level: str = "INFO"


settings = Settings()  # type: ignore[call-arg]
