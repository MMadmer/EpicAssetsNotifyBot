from __future__ import annotations

from typing import Any, TypedDict, cast

from .localization import Localizer
from .scraper import Asset, DeadlineInfo


class ChannelSubscription(TypedDict):
    id: int
    shown_assets: bool
    locale: str


class GuildConfig(TypedDict):
    guild_id: int
    channel_id: int | None
    thread_id: int | None
    shown_assets: bool
    locale: str
    mention_role_id: int | None
    enabled: bool
    include_images: bool


class UserProfile(TypedDict):
    id: int
    shown_assets: bool
    locale: str
    subscribed: bool


StoredDeadline = DeadlineInfo | str | None


class StateNormalizer:
    def __init__(self, localizer: Localizer, base_locale: str):
        self.localizer = localizer
        self.base_locale = base_locale

    def normalize_channels(self, payload: Any) -> list[ChannelSubscription]:
        if not isinstance(payload, list):
            return []

        normalized_channels: dict[int, ChannelSubscription] = {}
        for item in payload:
            if not isinstance(item, dict):
                continue

            channel_id = item.get("id")
            if not isinstance(channel_id, int):
                continue

            locale = item.get("locale") if isinstance(item.get("locale"), str) else None
            normalized_channels[channel_id] = {
                "id": channel_id,
                "shown_assets": bool(item.get("shown_assets", False)),
                "locale": self.localizer.normalize_locale(locale) or self.base_locale,
            }

        return list(normalized_channels.values())

    def normalize_guild_configs(self, payload: Any) -> list[GuildConfig]:
        if not isinstance(payload, list):
            return []

        normalized_configs: dict[int, GuildConfig] = {}
        for item in payload:
            if not isinstance(item, dict):
                continue

            guild_id = item.get("guild_id")
            if not isinstance(guild_id, int):
                continue

            locale = item.get("locale") if isinstance(item.get("locale"), str) else None
            channel_id = item.get("channel_id")
            thread_id = item.get("thread_id")
            mention_role_id = item.get("mention_role_id")

            normalized_configs[guild_id] = {
                "guild_id": guild_id,
                "channel_id": channel_id if isinstance(channel_id, int) else None,
                "thread_id": thread_id if isinstance(thread_id, int) else None,
                "shown_assets": bool(item.get("shown_assets", False)),
                "locale": self.localizer.normalize_locale(locale) or self.base_locale,
                "mention_role_id": mention_role_id if isinstance(mention_role_id, int) else None,
                "enabled": bool(item.get("enabled", True)),
                "include_images": bool(item.get("include_images", True)),
            }

        return list(normalized_configs.values())

    def normalize_user_profiles(self, payload: Any) -> list[UserProfile]:
        if not isinstance(payload, list):
            return []

        normalized_profiles: dict[int, UserProfile] = {}
        for item in payload:
            if not isinstance(item, dict):
                continue

            user_id = item.get("id")
            if not isinstance(user_id, int):
                continue

            locale = item.get("locale") if isinstance(item.get("locale"), str) else None
            normalized_profiles[user_id] = {
                "id": user_id,
                "shown_assets": bool(item.get("shown_assets", False)),
                "locale": self.localizer.normalize_locale(locale) or self.base_locale,
                "subscribed": bool(item.get("subscribed", True)),
            }

        return list(normalized_profiles.values())

    def normalize_assets(self, payload: Any) -> list[Asset]:
        if not isinstance(payload, list):
            return []

        normalized_assets: list[Asset] = []
        seen_links: set[str] = set()
        for item in payload:
            if not isinstance(item, dict):
                continue

            link = item.get("link")
            if not isinstance(link, str) or not link:
                continue
            if link in seen_links:
                continue
            seen_links.add(link)

            name = item.get("name")
            image = item.get("image")
            normalized_assets.append(
                {
                    "name": name if isinstance(name, str) and name else None,
                    "link": link,
                    "image": image if isinstance(image, str) and image else None,
                }
            )

        return normalized_assets

    def normalize_deadline(self, payload: Any) -> StoredDeadline:
        if isinstance(payload, str):
            return payload or None

        if payload is None:
            return None

        if not isinstance(payload, dict):
            return None

        required_int_fields = ("day", "month", "year", "hour", "minute")
        normalized_deadline: dict[str, int | str] = {}

        for field in required_int_fields:
            value = payload.get(field)
            if not isinstance(value, int):
                return None
            normalized_deadline[field] = value

        gmt_offset = payload.get("gmt_offset")
        if not isinstance(gmt_offset, str) or not gmt_offset:
            return None
        normalized_deadline["gmt_offset"] = gmt_offset

        return cast(StoredDeadline, normalized_deadline)
