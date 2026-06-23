"""Чистая логика членства в канале — вынесена для юнит-тестов."""
from __future__ import annotations

_SUBSCRIBED = {"creator", "administrator", "member"}
_NOT_SUBSCRIBED = {"left", "kicked"}


def membership_allows(status: str, is_member: bool | None) -> bool:
    """left/kicked -> нет; restricted -> только при is_member=True; иначе по статусу."""
    if status in _NOT_SUBSCRIBED:
        return False
    if status == "restricted":
        return bool(is_member)
    return status in _SUBSCRIBED
