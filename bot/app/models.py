"""Локальный леджер подписчиков (SQLite). Авторитетные ключи живут в Postgres
агента; здесь только связка tg_id ↔ sub_path ↔ статус для идемпотентного /start
и реконсиляции воркером."""
from __future__ import annotations

from datetime import datetime

from sqlalchemy import BigInteger, DateTime, String, func
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column


class Base(DeclarativeBase):
    pass


ACTIVE = "active"
REVOKED = "revoked"


class Subscriber(Base):
    __tablename__ = "subscribers"

    telegram_id: Mapped[int] = mapped_column(BigInteger, primary_key=True, autoincrement=False)
    username: Mapped[str | None] = mapped_column(String(255), nullable=True)
    uuid: Mapped[str] = mapped_column(String(64), default="")
    sub_token: Mapped[str] = mapped_column(String(128), default="")
    sub_path: Mapped[str] = mapped_column(String(160), default="")   # "/sub/<token>"
    status: Mapped[str] = mapped_column(String(16), default=ACTIVE)

    joined_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now())
    revoked_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    last_checked: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)

    def public_link(self, base: str) -> str:
        return f"{base.rstrip('/')}{self.sub_path}"
