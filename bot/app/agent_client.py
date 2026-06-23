"""HTTP-клиент к API vlr-main-agent."""
from __future__ import annotations

import httpx


class AgentError(Exception):
    pass


class AgentClient:
    def __init__(self, base_url: str, token: str, timeout: float = 20.0) -> None:
        self._c = httpx.AsyncClient(
            base_url=base_url.rstrip("/"),
            timeout=timeout,
            headers={"Authorization": f"Bearer {token}"},
        )

    async def create_user(
        self,
        node_id: str,
        telegram_id: int,
        expires_in: str = "",
        label: str = "",
        title: str = "",
        profile: str = "",
    ) -> dict:
        """POST /v1/users -> {uuid, link, subscription, sub_token, sub_path, expires_at?}."""
        payload: dict = {"node_id": node_id, "telegram_id": telegram_id}
        if expires_in:
            payload["expires_in"] = expires_in
        if label:
            payload["label"] = label
        if title:
            payload["title"] = title
        if profile:
            payload["profile"] = profile
        r = await self._c.post("/v1/users", json=payload)
        if r.status_code not in (200, 201):
            raise AgentError(f"create_user {r.status_code}: {r.text}")
        return r.json()

    async def delete_user(self, ref: str) -> None:
        """DELETE /v1/users/<ref>. 404 = уже нет."""
        r = await self._c.delete(f"/v1/users/{ref}")
        if r.status_code not in (200, 404):
            raise AgentError(f"delete_user {r.status_code}: {r.text}")

    async def aclose(self) -> None:
        await self._c.aclose()
