from __future__ import annotations

import asyncio
import re
from dataclasses import dataclass
from datetime import datetime, timedelta
from io import BytesIO
from typing import Any

import aiohttp
import discord
from discord.ext import commands
from loguru import logger

from .config import get_data_folder, get_database_url, load_legacy_snapshot
from .database import DatabaseManager, DatabaseSnapshot
from .localization import Localizer
from .scraper import Asset, get_free_assets
from .state import ChannelSubscription, StateNormalizer, StoredDeadline, UserProfile
from .storage import ensure_directory


@dataclass(frozen=True)
class AttachmentPayload:
    filename: str
    content: bytes


def is_admin(ctx: commands.Context) -> bool:
    return ctx.guild is not None and ctx.author.guild_permissions.administrator


def is_dm(ctx: commands.Context) -> bool:
    return ctx.guild is None


class EpicAssetsNotifyBot(commands.Bot):
    def __init__(self, command_prefix: str, token: str, localizer: Localizer):
        intents = discord.Intents.default()
        intents.message_content = True
        super().__init__(command_prefix=command_prefix, intents=intents)

        self.token = token
        self.localizer = localizer
        self.base_locale = localizer.normalize_locale(localizer.locale) or localizer.default_locale
        self.state_normalizer = StateNormalizer(localizer=localizer, base_locale=self.base_locale)
        self.data_folder = get_data_folder()
        self.database = DatabaseManager(get_database_url(self.data_folder))

        self.subscribed_channels: list[ChannelSubscription] = []
        self.user_profiles: list[UserProfile] = []
        self.assets_list: list[Asset] = []
        self.deadline_data: StoredDeadline = None

        self.next_check_time = None
        self.delete_after = 10
        self.backup_delay = 900
        self.message_delay = 0.5
        self._background_tasks_started = False

        self.add_commands()

    async def setup_hook(self) -> None:
        if not self.data_folder.exists():
            logger.info(f"Creating data folder at {self.data_folder}")
        ensure_directory(self.data_folder)

        await self.database.initialize()
        await self.database.migrate_legacy_snapshot_if_empty(
            load_legacy_snapshot(self.data_folder, self.state_normalizer)
        )
        await self._reload_state_from_database()

    async def on_ready(self):
        logger.info(f"Logged in as {self.user}")
        if self._background_tasks_started:
            return

        self._background_tasks_started = True
        asyncio.create_task(self.set_daily_check())
        asyncio.create_task(self.backup_loop())

    async def close(self) -> None:
        await self.database.dispose()
        await super().close()

    def run_bot(self):
        if not self.data_folder.exists():
            logger.info(f"Creating data folder at {self.data_folder}")
        ensure_directory(self.data_folder)

        logger.info("Starting bot...")
        self.run(self.token)

    def _normalize_channels(self, payload: Any) -> list[ChannelSubscription]:
        return self.state_normalizer.normalize_channels(payload)

    def _normalize_user_profiles(self, payload: Any) -> list[UserProfile]:
        return self.state_normalizer.normalize_user_profiles(payload)

    def _normalize_assets(self, payload: Any) -> list[Asset]:
        return self.state_normalizer.normalize_assets(payload)

    def _normalize_deadline(self, payload: Any) -> StoredDeadline:
        return self.state_normalizer.normalize_deadline(payload)

    async def _reload_state_from_database(self) -> None:
        snapshot = await self.database.load_snapshot()
        self.subscribed_channels = self._normalize_channels(snapshot.channels)
        self.user_profiles = self._normalize_user_profiles(snapshot.user_profiles)
        self.assets_list = self._normalize_assets(snapshot.assets)
        self.deadline_data = self._normalize_deadline(snapshot.deadline)

    def _find_channel_subscription(self, channel_id: int) -> ChannelSubscription | None:
        for channel in self.subscribed_channels:
            if channel["id"] == channel_id:
                return channel
        return None

    def _find_user_profile(self, user_id: int) -> UserProfile | None:
        for profile in self.user_profiles:
            if profile["id"] == user_id:
                return profile
        return None

    def _ensure_user_profile(self, user_id: int) -> UserProfile:
        profile = self._find_user_profile(user_id)
        if profile is not None:
            return profile

        profile: UserProfile = {
            "id": user_id,
            "shown_assets": False,
            "locale": self.base_locale,
            "subscribed": False,
        }
        self.user_profiles.append(profile)
        return profile

    def _get_channel_locale(self, channel_id: int) -> str:
        channel = self._find_channel_subscription(channel_id)
        if channel is None:
            return self.base_locale
        return channel["locale"]

    def _get_user_locale(self, user_id: int) -> str:
        profile = self._find_user_profile(user_id)
        if profile is None:
            return self.base_locale
        return profile["locale"]

    def _get_localizer(
        self,
        *,
        user_id: int | None = None,
        channel_id: int | None = None,
        locale: str | None = None,
    ) -> Localizer:
        effective_locale = locale
        if effective_locale is None and channel_id is not None:
            effective_locale = self._get_channel_locale(channel_id)
        if effective_locale is None and user_id is not None:
            effective_locale = self._get_user_locale(user_id)
        if effective_locale is None:
            effective_locale = self.base_locale
        return self.localizer.for_locale(effective_locale)

    def _localizer_for_ctx(self, ctx: commands.Context) -> Localizer:
        if is_dm(ctx):
            return self._get_localizer(user_id=ctx.author.id)
        return self._get_localizer(channel_id=ctx.channel.id)

    def _subscribed_users(self) -> list[UserProfile]:
        return [profile for profile in self.user_profiles if profile["subscribed"]]

    def _deadline_signature(self, deadline: StoredDeadline) -> tuple[Any, ...] | str:
        if isinstance(deadline, dict):
            return (
                deadline["year"],
                deadline["month"],
                deadline["day"],
                deadline["hour"],
                deadline["minute"],
                deadline["gmt_offset"],
            )
        return deadline or ""

    def _format_deadline_suffix(self, localizer: Localizer) -> str:
        if isinstance(self.deadline_data, str):
            return self.deadline_data

        if not isinstance(self.deadline_data, dict):
            return ""

        return localizer.t(
            "deadline.until",
            day=self.deadline_data["day"],
            month_name=localizer.month_name(self.deadline_data["month"], context="format"),
            hour=self.deadline_data["hour"],
            minute=self.deadline_data["minute"],
            gmt_offset=self.deadline_data["gmt_offset"],
        )

    def _compose_header(self, localizer: Localizer) -> str:
        month_name = localizer.month_name(datetime.now().month, context="standalone")
        deadline_suffix = self._format_deadline_suffix(localizer)
        if deadline_suffix:
            return localizer.t(
                "header.with_deadline",
                month_name=month_name,
                deadline_suffix=deadline_suffix,
            )
        return localizer.t("header.without_deadline", month_name=month_name)

    def _compose_message(self, assets: list[Asset], localizer: Localizer) -> str:
        message = self._compose_header(localizer)
        for asset in assets:
            link = asset.get("link")
            if not link:
                continue
            asset_name = asset.get("name") or localizer.t("assets.untitled")
            message += f"- [{asset_name}](<{link}>)\n"
        return message

    async def _build_attachments(self, assets: list[Asset]) -> list[AttachmentPayload]:
        attachments: list[AttachmentPayload] = []

        async with aiohttp.ClientSession() as session:
            for asset in assets:
                image_url = asset.get("image")
                if not image_url:
                    continue

                try:
                    async with session.get(image_url, timeout=30) as response:
                        image_data = await response.read()
                    safe_name = re.sub(r'[\\/*?:"<>|]+', "_", asset.get("name") or "image")[:100] or "image"
                    attachments.append(
                        AttachmentPayload(filename=f"{safe_name}.png", content=image_data)
                    )
                except Exception as exc:
                    logger.warning(f"Image fetch failed for {asset.get('link')}: {exc}")

        return attachments

    def _to_discord_files(self, attachments: list[AttachmentPayload]) -> list[discord.File]:
        return [
            discord.File(BytesIO(attachment.content), filename=attachment.filename)
            for attachment in attachments
        ]

    async def _send_asset_message(self, target, message: str, attachments: list[AttachmentPayload]) -> None:
        files = self._to_discord_files(attachments)
        if files:
            await target.send(message, files=files)
            return
        await target.send(message)

    async def _send_temporary_message(self, ctx: commands.Context, content: str) -> None:
        sent_message = await ctx.send(content)
        await asyncio.sleep(self.delete_after)
        await sent_message.delete()

    def _locale_choices(self) -> list[str]:
        choices: list[str] = []
        for locale_code in self.localizer.available_locales():
            short_code = locale_code.split("-")[0].lower()
            if short_code not in choices:
                choices.append(short_code)
        return choices

    def _available_locales_label(self) -> str:
        labels: list[str] = []
        seen_short_codes: set[str] = set()

        for locale_code, locale_name in self.localizer.available_locales().items():
            short_code = locale_code.split("-")[0].lower()
            if short_code in seen_short_codes:
                labels.append(f"{locale_code} ({locale_name})")
                continue

            seen_short_codes.add(short_code)
            labels.append(f"{short_code} ({locale_name})")

        return ", ".join(labels)

    def _locale_command_usage(self, prefix: str) -> str:
        return f"{prefix}lang <{'|'.join(self._locale_choices())}>"

    def add_commands(self):
        @self.command(name="sub")
        async def subscribe(ctx: commands.Context):
            localizer = self._localizer_for_ctx(ctx)
            permission_denied = localizer.t("errors.permission_denied")
            if not is_admin(ctx) and not is_dm(ctx):
                await ctx.send(permission_denied)
                return

            if is_dm(ctx):
                user_id = ctx.author.id
                profile = self._ensure_user_profile(user_id)
                if profile["subscribed"]:
                    await ctx.send(localizer.t("subscribe.dm.already"))
                    return

                profile["subscribed"] = True
                profile["shown_assets"] = False
                await ctx.send(localizer.t("subscribe.dm.success"))
                logger.info(f"User {ctx.author} subscribed to asset updates.")

                if self.assets_list and not profile["shown_assets"]:
                    message = self._compose_message(
                        self.assets_list,
                        self._get_localizer(locale=profile["locale"]),
                    )
                    attachments = await self._build_attachments(self.assets_list)
                    user = await self.fetch_user(user_id)
                    await self._send_asset_message(user, message, attachments)
                    profile["shown_assets"] = True

                await self.backup_data()
                return

            channel_id = ctx.channel.id
            if self._find_channel_subscription(channel_id) is not None:
                await ctx.send(localizer.t("subscribe.channel.already"))
                return

            channel_subscription: ChannelSubscription = {
                "id": channel_id,
                "shown_assets": False,
                "locale": self.base_locale,
            }
            self.subscribed_channels.append(channel_subscription)
            await ctx.send(localizer.t("subscribe.channel.success", channel_name=ctx.channel.name))
            logger.info(f"Channel {ctx.channel.name} subscribed to asset updates.")

            if self.assets_list and not channel_subscription["shown_assets"]:
                message = self._compose_message(
                    self.assets_list,
                    self._get_localizer(locale=channel_subscription["locale"]),
                )
                attachments = await self._build_attachments(self.assets_list)
                channel = self.get_channel(channel_id)
                if channel:
                    await self._send_asset_message(channel, message, attachments)
                    channel_subscription["shown_assets"] = True

            await self.backup_data()

        @self.command(name="unsub")
        async def unsubscribe(ctx: commands.Context):
            localizer = self._localizer_for_ctx(ctx)
            permission_denied = localizer.t("errors.permission_denied")
            if not is_admin(ctx) and not is_dm(ctx):
                await ctx.send(permission_denied)
                return

            if is_dm(ctx):
                user_id = ctx.author.id
                profile = self._find_user_profile(user_id)
                if profile and profile["subscribed"]:
                    profile["subscribed"] = False
                    profile["shown_assets"] = False
                    await ctx.send(localizer.t("unsubscribe.success"))
                    logger.info(f"User {ctx.author} unsubscribed from asset updates.")
                    await self.backup_data()
                    return

                await ctx.send(localizer.t("unsubscribe.dm.not_subscribed"))
                return

            channel_id = ctx.channel.id
            channel = self._find_channel_subscription(channel_id)
            if channel is not None:
                self.subscribed_channels.remove(channel)
                await ctx.send(localizer.t("unsubscribe.success"))
                logger.info(f"Channel {ctx.channel.name} unsubscribed from asset updates.")
                await self.backup_data()
                return

            await ctx.send(localizer.t("unsubscribe.channel.not_subscribed"))

        @self.command(name="time")
        async def time_left(ctx: commands.Context):
            localizer = self._localizer_for_ctx(ctx)
            delete_hint = localizer.t("time.delete_hint", delete_after=self.delete_after)

            if self.next_check_time:
                now = datetime.now()
                time_remaining = self.next_check_time - now
                hours, remainder = divmod(time_remaining.seconds, 3600)
                minutes, seconds = divmod(remainder, 60)
                message = "\n".join(
                    [
                        localizer.t(
                            "time.remaining",
                            hours=hours,
                            minutes=minutes,
                            seconds=seconds,
                        ),
                        delete_hint,
                    ]
                )
                await self._send_temporary_message(ctx, message)
                return

            message = "\n".join([localizer.t("time.no_schedule"), delete_hint])
            await self._send_temporary_message(ctx, message)

        @self.command(name="lang", aliases=["locale", "l"])
        async def change_locale(ctx: commands.Context, locale: str | None = None):
            usage = self._locale_command_usage(ctx.clean_prefix)

            if is_dm(ctx):
                localizer = self._get_localizer(user_id=ctx.author.id)
                current_locale = self._get_user_locale(ctx.author.id)

                if locale is None:
                    message = "\n".join(
                        [
                            localizer.t(
                                "locale.dm.current",
                                locale_name=self.localizer.locale_name(current_locale),
                                locale_code=current_locale,
                            ),
                            localizer.t("locale.available", locales=self._available_locales_label()),
                            localizer.t("locale.usage", command=usage),
                        ]
                    )
                    await ctx.send(message)
                    return

                resolved_locale = self.localizer.normalize_locale(locale)
                if resolved_locale is None:
                    message = "\n".join(
                        [
                            localizer.t("locale.invalid", input_value=locale),
                            localizer.t("locale.available", locales=self._available_locales_label()),
                            localizer.t("locale.usage", command=usage),
                        ]
                    )
                    await ctx.send(message)
                    return

                if current_locale == resolved_locale:
                    await ctx.send(
                        localizer.t(
                            "locale.dm.already",
                            locale_name=self.localizer.locale_name(resolved_locale),
                            locale_code=resolved_locale,
                        )
                    )
                    return

                profile = self._ensure_user_profile(ctx.author.id)
                profile["locale"] = resolved_locale
                await self.backup_data()

                new_localizer = self._get_localizer(locale=resolved_locale)
                await ctx.send(
                    new_localizer.t(
                        "locale.dm.changed",
                        locale_name=self.localizer.locale_name(resolved_locale),
                        locale_code=resolved_locale,
                    )
                )
                return

            localizer = self._get_localizer(channel_id=ctx.channel.id)
            channel = self._find_channel_subscription(ctx.channel.id)
            current_locale = self._get_channel_locale(ctx.channel.id)

            if locale is None:
                message = "\n".join(
                    [
                        localizer.t(
                            "locale.channel.current",
                            locale_name=self.localizer.locale_name(current_locale),
                            locale_code=current_locale,
                        ),
                        localizer.t("locale.available", locales=self._available_locales_label()),
                        localizer.t("locale.usage", command=usage),
                    ]
                )
                await ctx.send(message)
                return

            if not is_admin(ctx):
                await ctx.send(localizer.t("errors.permission_denied"))
                return

            resolved_locale = self.localizer.normalize_locale(locale)
            if resolved_locale is None:
                message = "\n".join(
                    [
                        localizer.t("locale.invalid", input_value=locale),
                        localizer.t("locale.available", locales=self._available_locales_label()),
                        localizer.t("locale.usage", command=usage),
                    ]
                )
                await ctx.send(message)
                return

            if channel is None:
                message = "\n".join(
                    [
                        localizer.t("locale.channel.subscription_required"),
                        localizer.t("locale.usage", command=usage),
                    ]
                )
                await ctx.send(message)
                return

            if current_locale == resolved_locale:
                await ctx.send(
                    localizer.t(
                        "locale.channel.already",
                        locale_name=self.localizer.locale_name(resolved_locale),
                        locale_code=resolved_locale,
                    )
                )
                return

            channel["locale"] = resolved_locale
            await self.backup_data()

            new_localizer = self._get_localizer(locale=resolved_locale)
            await ctx.send(
                new_localizer.t(
                    "locale.channel.changed",
                    locale_name=self.localizer.locale_name(resolved_locale),
                    locale_code=resolved_locale,
                )
            )

        @subscribe.error
        @unsubscribe.error
        async def on_command_error(ctx: commands.Context, error: commands.CommandError):
            if isinstance(error, commands.MissingPermissions):
                await ctx.send(self._localizer_for_ctx(ctx).t("errors.permission_denied"))

    async def set_daily_check(self):
        while True:
            self.next_check_time = datetime.now() + timedelta(days=1)
            try:
                await self.check_and_notify_assets()
            except Exception as exc:
                logger.exception(f"Scheduled asset check failed: {exc}")
            await asyncio.sleep(24 * 60 * 60)

    async def check_and_notify_assets(self):
        assets, deadline = await get_free_assets()
        if not assets:
            return

        def asset_ids(items: list[Asset]) -> set[str]:
            return {asset["link"] for asset in items if asset.get("link")}

        new_ids = asset_ids(assets)
        old_ids = asset_ids(self.assets_list)
        deadline_changed = self._deadline_signature(deadline) != self._deadline_signature(
            self.deadline_data
        )

        added = new_ids - old_ids

        if not deadline_changed and not added and new_ids.issubset(old_ids) and new_ids != old_ids:
            logger.warning("Shrink-only change detected, likely transient scrape issue. Skipping update.")
            return

        if not deadline_changed and new_ids == old_ids:
            return

        self.assets_list = assets
        self.deadline_data = deadline

        attachments = await self._build_attachments(assets)
        message_cache: dict[str, str] = {}

        for channel in self.subscribed_channels:
            channel_locale = channel["locale"]
            channel_message = message_cache.get(channel_locale)
            if channel_message is None:
                channel_message = self._compose_message(
                    assets,
                    self._get_localizer(locale=channel_locale),
                )
                message_cache[channel_locale] = channel_message

            channel_obj = self.get_channel(channel["id"])
            if channel_obj:
                await self._send_asset_message(channel_obj, channel_message, attachments)
                channel["shown_assets"] = True
            await asyncio.sleep(self.message_delay)

        for user in self._subscribed_users():
            user_locale = user["locale"]
            user_message = message_cache.get(user_locale)
            if user_message is None:
                user_message = self._compose_message(
                    assets,
                    self._get_localizer(locale=user_locale),
                )
                message_cache[user_locale] = user_message

            try:
                user_obj = await self.fetch_user(user["id"])
                await self._send_asset_message(user_obj, user_message, attachments)
                user["shown_assets"] = True
            except Exception as exc:
                logger.warning(f"DM failed for {user['id']}: {exc}")
            await asyncio.sleep(self.message_delay)

        await self.backup_data()

    async def backup_loop(self):
        while True:
            try:
                await self.backup_data()
            except Exception as exc:
                logger.exception(f"Scheduled database sync failed: {exc}")
            await asyncio.sleep(self.backup_delay)

    async def backup_data(self):
        await self.database.save_snapshot(
            DatabaseSnapshot(
                channels=self.subscribed_channels,
                user_profiles=self.user_profiles,
                assets=self.assets_list,
                deadline=self.deadline_data,
            )
        )
        logger.info("Persisted bot state to database.")
