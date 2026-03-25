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
from .state import (
    ChannelSubscription,
    GuildConfig,
    StateNormalizer,
    StoredDeadline,
    UserProfile,
)
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

        self.guild_configs: list[GuildConfig] = []
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
        await self.database.import_legacy_snapshot_if_empty(
            load_legacy_snapshot(self.data_folder, self.state_normalizer)
        )
        await self._reload_state_from_database()

    async def on_ready(self):
        logger.info(f"Logged in as {self.user}")
        await self._migrate_legacy_server_configs()

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

    def _normalize_guild_configs(self, payload: Any) -> list[GuildConfig]:
        return self.state_normalizer.normalize_guild_configs(payload)

    def _normalize_user_profiles(self, payload: Any) -> list[UserProfile]:
        return self.state_normalizer.normalize_user_profiles(payload)

    def _normalize_assets(self, payload: Any) -> list[Asset]:
        return self.state_normalizer.normalize_assets(payload)

    def _normalize_deadline(self, payload: Any) -> StoredDeadline:
        return self.state_normalizer.normalize_deadline(payload)

    async def _reload_state_from_database(self) -> None:
        snapshot = await self.database.load_snapshot()
        self.guild_configs = self._normalize_guild_configs(snapshot.guild_configs)
        self.user_profiles = self._normalize_user_profiles(snapshot.user_profiles)
        self.assets_list = self._normalize_assets(snapshot.assets)
        self.deadline_data = self._normalize_deadline(snapshot.deadline)

    async def _migrate_legacy_server_configs(self) -> None:
        if self.guild_configs:
            return

        legacy_channels = self._normalize_channels(
            await self.database.load_legacy_channel_subscriptions()
        )
        if not legacy_channels:
            return

        converted_configs: dict[int, GuildConfig] = {}
        duplicate_channels: dict[int, list[int]] = {}
        unresolved_channels: list[int] = []

        for legacy_channel in legacy_channels:
            channel_obj = await self._resolve_channel_object(legacy_channel["id"])
            if channel_obj is None or getattr(channel_obj, "guild", None) is None:
                unresolved_channels.append(legacy_channel["id"])
                continue

            guild_id = channel_obj.guild.id
            if guild_id in converted_configs:
                duplicate_channels.setdefault(guild_id, []).append(legacy_channel["id"])
                continue

            converted_configs[guild_id] = {
                "guild_id": guild_id,
                "channel_id": channel_obj.id,
                "thread_id": None,
                "shown_assets": legacy_channel["shown_assets"],
                "locale": legacy_channel["locale"],
                "mention_role_id": None,
                "enabled": True,
                "include_images": True,
            }

        if not converted_configs:
            return

        self.guild_configs = list(converted_configs.values())
        await self.backup_data()
        logger.info(
            f"Migrated {len(converted_configs)} legacy channel subscriptions into guild configs."
        )

        if duplicate_channels:
            logger.warning(
                "Multiple legacy subscribed channels were found in some guilds. "
                f"Only the first channel per guild was migrated: {duplicate_channels}"
            )

        if unresolved_channels:
            logger.warning(
                "Some legacy subscribed channels could not be resolved and were left untouched: "
                f"{unresolved_channels}"
            )

    def _find_guild_config(self, guild_id: int) -> GuildConfig | None:
        for config in self.guild_configs:
            if config["guild_id"] == guild_id:
                return config
        return None

    def _ensure_guild_config(self, guild_id: int) -> GuildConfig:
        config = self._find_guild_config(guild_id)
        if config is not None:
            return config

        config = {
            "guild_id": guild_id,
            "channel_id": None,
            "thread_id": None,
            "shown_assets": False,
            "locale": self.base_locale,
            "mention_role_id": None,
            "enabled": False,
            "include_images": True,
        }
        self.guild_configs.append(config)
        return config

    def _set_guild_target(
        self,
        config: GuildConfig,
        *,
        channel_id: int | None,
        thread_id: int | None,
    ) -> bool:
        changed = config["channel_id"] != channel_id or config["thread_id"] != thread_id
        config["channel_id"] = channel_id
        config["thread_id"] = thread_id
        if changed:
            config["shown_assets"] = False
        return changed

    def _find_user_profile(self, user_id: int) -> UserProfile | None:
        for profile in self.user_profiles:
            if profile["id"] == user_id:
                return profile
        return None

    def _ensure_user_profile(self, user_id: int) -> UserProfile:
        profile = self._find_user_profile(user_id)
        if profile is not None:
            return profile

        profile = {
            "id": user_id,
            "shown_assets": False,
            "locale": self.base_locale,
            "subscribed": False,
        }
        self.user_profiles.append(profile)
        return profile

    def _get_context_target_ids(self, ctx: commands.Context) -> tuple[int | None, int | None]:
        if is_dm(ctx):
            return None, None

        if isinstance(ctx.channel, discord.Thread):
            return ctx.channel.parent_id, ctx.channel.id

        return ctx.channel.id, None

    def _get_guild_locale(self, guild_id: int) -> str:
        config = self._find_guild_config(guild_id)
        if config is None:
            return self.base_locale
        return config["locale"]

    def _get_user_locale(self, user_id: int) -> str:
        profile = self._find_user_profile(user_id)
        if profile is None:
            return self.base_locale
        return profile["locale"]

    def _get_localizer(
        self,
        *,
        user_id: int | None = None,
        guild_id: int | None = None,
        locale: str | None = None,
    ) -> Localizer:
        effective_locale = locale
        if effective_locale is None and guild_id is not None:
            effective_locale = self._get_guild_locale(guild_id)
        if effective_locale is None and user_id is not None:
            effective_locale = self._get_user_locale(user_id)
        if effective_locale is None:
            effective_locale = self.base_locale
        return self.localizer.for_locale(effective_locale)

    def _localizer_for_ctx(self, ctx: commands.Context) -> Localizer:
        if is_dm(ctx):
            return self._get_localizer(user_id=ctx.author.id)
        return self._get_localizer(guild_id=ctx.guild.id)

    def _subscribed_users(self) -> list[UserProfile]:
        return [profile for profile in self.user_profiles if profile["subscribed"]]

    def _enabled_guild_configs(self) -> list[GuildConfig]:
        return [
            config
            for config in self.guild_configs
            if config["enabled"] and config["channel_id"] is not None
        ]

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

    def _compose_delivery_content(self, config: GuildConfig, message: str) -> str:
        if config["mention_role_id"] is None:
            return message

        return f"<@&{config['mention_role_id']}>\n{message}"

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

    async def _send_asset_message(
        self,
        target,
        message: str,
        attachments: list[AttachmentPayload],
    ) -> None:
        files = self._to_discord_files(attachments)
        allowed_mentions = discord.AllowedMentions(roles=True)
        if files:
            await target.send(message, files=files, allowed_mentions=allowed_mentions)
            return
        await target.send(message, allowed_mentions=allowed_mentions)

    async def _send_temporary_message(self, ctx: commands.Context, content: str) -> None:
        sent_message = await ctx.send(content)
        await asyncio.sleep(self.delete_after)
        await sent_message.delete()

    async def _resolve_channel_object(self, channel_id: int):
        channel = self.get_channel(channel_id)
        if channel is not None:
            return channel

        try:
            return await self.fetch_channel(channel_id)
        except Exception as exc:
            logger.warning(f"Failed to resolve channel {channel_id}: {exc}")
            return None

    async def _resolve_target(self, config: GuildConfig):
        target_id = config["thread_id"] or config["channel_id"]
        if target_id is None:
            return None

        return await self._resolve_channel_object(target_id)

    def _format_target_label(self, config: GuildConfig, localizer: Localizer) -> str:
        if config["thread_id"] is not None:
            return localizer.t("server.target.thread", thread_id=config["thread_id"])
        if config["channel_id"] is not None:
            return localizer.t("server.target.channel", channel_id=config["channel_id"])
        return localizer.t("server.target.none")

    def _format_role_label(self, config: GuildConfig, localizer: Localizer) -> str:
        if config["mention_role_id"] is None:
            return localizer.t("server.target.none")
        return f"<@&{config['mention_role_id']}>"

    def _format_bool_label(self, value: bool, localizer: Localizer) -> str:
        return localizer.t("common.enabled" if value else "common.disabled")

    def _time_remaining_parts(self) -> tuple[int, int, int] | None:
        if self.next_check_time is None:
            return None

        time_remaining = max(self.next_check_time - datetime.now(), timedelta())
        total_seconds = int(time_remaining.total_seconds())
        hours, remainder = divmod(total_seconds, 3600)
        minutes, seconds = divmod(remainder, 60)
        return hours, minutes, seconds

    def _format_next_check_label(self, localizer: Localizer) -> str:
        remaining = self._time_remaining_parts()
        if remaining is None:
            return localizer.t("server.settings.no_next_check")

        hours, minutes, seconds = remaining
        return localizer.t(
            "time.remaining",
            hours=hours,
            minutes=minutes,
            seconds=seconds,
        )

    def _settings_message(self, config: GuildConfig | None, localizer: Localizer) -> str:
        if config is None:
            return "\n".join(
                [
                    localizer.t("server.settings.not_configured"),
                    localizer.t("server.settings.hint"),
                ]
            )

        return "\n".join(
            [
                localizer.t("server.settings.header"),
                localizer.t(
                    "server.settings.enabled",
                    value=self._format_bool_label(config["enabled"], localizer),
                ),
                localizer.t(
                    "server.settings.channel",
                    value=(
                        localizer.t("server.target.channel", channel_id=config["channel_id"])
                        if config["channel_id"] is not None
                        else localizer.t("server.target.none")
                    ),
                ),
                localizer.t(
                    "server.settings.thread",
                    value=(
                        localizer.t("server.target.thread", thread_id=config["thread_id"])
                        if config["thread_id"] is not None
                        else localizer.t("server.target.none")
                    ),
                ),
                localizer.t(
                    "server.settings.role",
                    value=self._format_role_label(config, localizer),
                ),
                localizer.t(
                    "server.settings.locale",
                    locale_name=self.localizer.locale_name(config["locale"]),
                    locale_code=config["locale"],
                ),
                localizer.t(
                    "server.settings.images",
                    value=self._format_bool_label(config["include_images"], localizer),
                ),
                localizer.t(
                    "server.settings.next_check",
                    value=self._format_next_check_label(localizer),
                ),
            ]
        )

    def _missing_target_permissions(self, target, config: GuildConfig) -> list[str]:
        guild = getattr(target, "guild", None)
        if guild is None:
            return []

        bot_member = guild.me or guild.get_member(self.user.id)
        if bot_member is None or not hasattr(target, "permissions_for"):
            return []

        permissions = target.permissions_for(bot_member)
        missing: list[str] = []

        if isinstance(target, discord.Thread):
            if not getattr(permissions, "send_messages_in_threads", True):
                missing.append("send_messages_in_threads")
        elif not getattr(permissions, "send_messages", True):
            missing.append("send_messages")

        if config["include_images"] and not getattr(permissions, "attach_files", True):
            missing.append("attach_files")

        return missing

    async def _send_current_assets_to_guild(self, config: GuildConfig) -> bool:
        if not config["enabled"] or not self.assets_list or config["shown_assets"]:
            return False

        target = await self._resolve_target(config)
        if target is None:
            logger.warning(
                f"Configured target for guild {config['guild_id']} could not be resolved."
            )
            return False

        localizer = self._get_localizer(guild_id=config["guild_id"])
        message = self._compose_message(self.assets_list, localizer)
        attachments = (
            await self._build_attachments(self.assets_list) if config["include_images"] else []
        )

        try:
            await self._send_asset_message(
                target,
                self._compose_delivery_content(config, message),
                attachments,
            )
        except Exception as exc:
            logger.warning(f"Initial asset send failed for guild {config['guild_id']}: {exc}")
            return False

        config["shown_assets"] = True
        return True

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
            if not is_admin(ctx) and not is_dm(ctx):
                await ctx.send(localizer.t("errors.permission_denied"))
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

            channel_id, thread_id = self._get_context_target_ids(ctx)
            config = self._ensure_guild_config(ctx.guild.id)
            if (
                config["enabled"]
                and config["channel_id"] == channel_id
                and config["thread_id"] == thread_id
            ):
                await ctx.send(localizer.t("subscribe.channel.already"))
                return

            self._set_guild_target(config, channel_id=channel_id, thread_id=thread_id)
            config["enabled"] = True
            await ctx.send(
                localizer.t(
                    "subscribe.channel.success",
                    channel_name=self._format_target_label(config, localizer),
                )
            )
            logger.info(
                f"Guild {ctx.guild.id} subscribed via {self._format_target_label(config, localizer)}."
            )

            await self._send_current_assets_to_guild(config)
            await self.backup_data()

        @self.command(name="unsub")
        async def unsubscribe(ctx: commands.Context):
            localizer = self._localizer_for_ctx(ctx)
            if not is_admin(ctx) and not is_dm(ctx):
                await ctx.send(localizer.t("errors.permission_denied"))
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

            config = self._find_guild_config(ctx.guild.id)
            if config is None or not config["enabled"]:
                await ctx.send(localizer.t("unsubscribe.channel.not_subscribed"))
                return

            config["enabled"] = False
            config["shown_assets"] = False
            await ctx.send(localizer.t("unsubscribe.success"))
            logger.info(f"Guild {ctx.guild.id} disabled asset updates.")
            await self.backup_data()

        @self.command(name="enable", aliases=["on"])
        async def enable_updates(ctx: commands.Context):
            localizer = self._localizer_for_ctx(ctx)
            if is_dm(ctx):
                await subscribe(ctx)
                return

            if not is_admin(ctx):
                await ctx.send(localizer.t("errors.permission_denied"))
                return

            config = self._ensure_guild_config(ctx.guild.id)
            if config["channel_id"] is None:
                channel_id, thread_id = self._get_context_target_ids(ctx)
                self._set_guild_target(config, channel_id=channel_id, thread_id=thread_id)

            if config["enabled"]:
                await ctx.send(
                    localizer.t(
                        "server.enable.already",
                        target=self._format_target_label(config, localizer),
                    )
                )
                return

            config["enabled"] = True
            await ctx.send(
                localizer.t(
                    "server.enable.success",
                    target=self._format_target_label(config, localizer),
                )
            )
            await self._send_current_assets_to_guild(config)
            await self.backup_data()

        @self.command(name="disable", aliases=["off"])
        async def disable_updates(ctx: commands.Context):
            localizer = self._localizer_for_ctx(ctx)
            if is_dm(ctx):
                await unsubscribe(ctx)
                return

            if not is_admin(ctx):
                await ctx.send(localizer.t("errors.permission_denied"))
                return

            config = self._find_guild_config(ctx.guild.id)
            if config is None or not config["enabled"]:
                await ctx.send(localizer.t("server.disable.already"))
                return

            config["enabled"] = False
            config["shown_assets"] = False
            await ctx.send(localizer.t("server.disable.success"))
            await self.backup_data()

        @self.command(name="set-channel", aliases=["setchannel"])
        async def set_channel(ctx: commands.Context):
            localizer = self._localizer_for_ctx(ctx)
            if is_dm(ctx):
                await ctx.send(localizer.t("errors.guild_only"))
                return

            if not is_admin(ctx):
                await ctx.send(localizer.t("errors.permission_denied"))
                return

            channel_id, _thread_id = self._get_context_target_ids(ctx)
            config = self._ensure_guild_config(ctx.guild.id)
            changed = self._set_guild_target(config, channel_id=channel_id, thread_id=None)
            if not changed:
                await ctx.send(
                    localizer.t(
                        "server.channel.already",
                        target=self._format_target_label(config, localizer),
                    )
                )
                return

            await ctx.send(
                localizer.t(
                    "server.channel.updated",
                    target=self._format_target_label(config, localizer),
                )
            )
            await self._send_current_assets_to_guild(config)
            await self.backup_data()

        @self.command(name="set-thread", aliases=["setthread"])
        async def set_thread(ctx: commands.Context):
            localizer = self._localizer_for_ctx(ctx)
            if is_dm(ctx):
                await ctx.send(localizer.t("errors.guild_only"))
                return

            if not is_admin(ctx):
                await ctx.send(localizer.t("errors.permission_denied"))
                return

            if not isinstance(ctx.channel, discord.Thread):
                await ctx.send(localizer.t("server.thread.not_in_thread"))
                return

            config = self._ensure_guild_config(ctx.guild.id)
            changed = self._set_guild_target(
                config,
                channel_id=ctx.channel.parent_id,
                thread_id=ctx.channel.id,
            )
            if not changed:
                await ctx.send(
                    localizer.t(
                        "server.thread.already",
                        target=self._format_target_label(config, localizer),
                    )
                )
                return

            await ctx.send(
                localizer.t(
                    "server.thread.updated",
                    target=self._format_target_label(config, localizer),
                )
            )
            await self._send_current_assets_to_guild(config)
            await self.backup_data()

        @self.command(name="clear-thread", aliases=["clearthread"])
        async def clear_thread(ctx: commands.Context):
            localizer = self._localizer_for_ctx(ctx)
            if is_dm(ctx):
                await ctx.send(localizer.t("errors.guild_only"))
                return

            if not is_admin(ctx):
                await ctx.send(localizer.t("errors.permission_denied"))
                return

            config = self._find_guild_config(ctx.guild.id)
            if config is None or config["thread_id"] is None:
                await ctx.send(localizer.t("server.thread.already_cleared"))
                return

            self._set_guild_target(config, channel_id=config["channel_id"], thread_id=None)
            await ctx.send(
                localizer.t(
                    "server.thread.cleared",
                    target=self._format_target_label(config, localizer),
                )
            )
            await self._send_current_assets_to_guild(config)
            await self.backup_data()

        @self.command(name="set-role", aliases=["setrole"])
        async def set_role(ctx: commands.Context, role: discord.Role):
            localizer = self._localizer_for_ctx(ctx)
            if is_dm(ctx):
                await ctx.send(localizer.t("errors.guild_only"))
                return

            if not is_admin(ctx):
                await ctx.send(localizer.t("errors.permission_denied"))
                return

            config = self._ensure_guild_config(ctx.guild.id)
            if config["mention_role_id"] == role.id:
                await ctx.send(localizer.t("server.role.already", role=f"<@&{role.id}>"))
                return

            config["mention_role_id"] = role.id
            await ctx.send(localizer.t("server.role.updated", role=f"<@&{role.id}>"))
            await self.backup_data()

        @self.command(name="clear-role", aliases=["clearrole"])
        async def clear_role(ctx: commands.Context):
            localizer = self._localizer_for_ctx(ctx)
            if is_dm(ctx):
                await ctx.send(localizer.t("errors.guild_only"))
                return

            if not is_admin(ctx):
                await ctx.send(localizer.t("errors.permission_denied"))
                return

            config = self._find_guild_config(ctx.guild.id)
            if config is None or config["mention_role_id"] is None:
                await ctx.send(localizer.t("server.role.already_cleared"))
                return

            config["mention_role_id"] = None
            await ctx.send(localizer.t("server.role.cleared"))
            await self.backup_data()

        @self.command(name="images")
        async def toggle_images(ctx: commands.Context, mode: str | None = None):
            localizer = self._localizer_for_ctx(ctx)
            if is_dm(ctx):
                await ctx.send(localizer.t("errors.guild_only"))
                return

            if not is_admin(ctx):
                await ctx.send(localizer.t("errors.permission_denied"))
                return

            config = self._ensure_guild_config(ctx.guild.id)
            if mode is None:
                await ctx.send(
                    localizer.t(
                        "server.images.current",
                        value=self._format_bool_label(config["include_images"], localizer),
                    )
                )
                return

            normalized_mode = mode.strip().lower()
            mode_map = {
                "on": True,
                "off": False,
                "true": True,
                "false": False,
                "yes": True,
                "no": False,
                "1": True,
                "0": False,
            }
            if normalized_mode not in mode_map:
                await ctx.send(localizer.t("server.images.invalid"))
                return

            include_images = mode_map[normalized_mode]
            if config["include_images"] == include_images:
                await ctx.send(
                    localizer.t(
                        "server.images.already",
                        value=self._format_bool_label(config["include_images"], localizer),
                    )
                )
                return

            config["include_images"] = include_images
            await ctx.send(
                localizer.t(
                    "server.images.updated",
                    value=self._format_bool_label(include_images, localizer),
                )
            )
            await self.backup_data()

        @self.command(name="settings", aliases=["config"])
        async def settings(ctx: commands.Context):
            localizer = self._localizer_for_ctx(ctx)
            if is_dm(ctx):
                profile = self._find_user_profile(ctx.author.id)
                if profile is None:
                    await ctx.send(localizer.t("server.settings.dm_not_configured"))
                    return

                status = self._format_bool_label(profile["subscribed"], localizer)
                await ctx.send(
                    "\n".join(
                        [
                            localizer.t("server.settings.dm_header"),
                            localizer.t("server.settings.dm_status", value=status),
                            localizer.t(
                                "server.settings.locale",
                                locale_name=self.localizer.locale_name(profile["locale"]),
                                locale_code=profile["locale"],
                            ),
                        ]
                    )
                )
                return

            await ctx.send(
                self._settings_message(self._find_guild_config(ctx.guild.id), localizer)
            )

        @self.command(name="test")
        async def test_notification(ctx: commands.Context):
            localizer = self._localizer_for_ctx(ctx)
            if is_dm(ctx):
                await ctx.send(localizer.t("errors.guild_only"))
                return

            if not is_admin(ctx):
                await ctx.send(localizer.t("errors.permission_denied"))
                return

            config = self._find_guild_config(ctx.guild.id)
            if config is None:
                await ctx.send(localizer.t("server.test.not_configured"))
                return

            target = await self._resolve_target(config)
            if target is None:
                await ctx.send(localizer.t("server.test.target_missing"))
                return

            missing_permissions = self._missing_target_permissions(target, config)
            blocking_permissions = [
                permission
                for permission in missing_permissions
                if permission != "attach_files"
            ]
            if blocking_permissions:
                await ctx.send(
                    localizer.t(
                        "server.test.missing_permissions",
                        permissions=", ".join(blocking_permissions),
                    )
                )
                return

            test_body = localizer.t("server.test.body", guild_name=ctx.guild.name)
            try:
                await self._send_asset_message(
                    target,
                    self._compose_delivery_content(config, test_body),
                    [],
                )
            except Exception as exc:
                await ctx.send(localizer.t("server.test.failed", error=str(exc)))
                return

            confirmation = localizer.t(
                "server.test.sent",
                target=self._format_target_label(config, localizer),
            )
            if "attach_files" in missing_permissions:
                confirmation = "\n".join(
                    [
                        confirmation,
                        localizer.t(
                            "server.test.missing_permissions",
                            permissions="attach_files",
                        ),
                    ]
                )
            await ctx.send(confirmation)

        @self.command(name="time")
        async def time_left(ctx: commands.Context):
            localizer = self._localizer_for_ctx(ctx)
            delete_hint = localizer.t("time.delete_hint", delete_after=self.delete_after)

            remaining = self._time_remaining_parts()
            if remaining is not None:
                hours, minutes, seconds = remaining
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

            localizer = self._get_localizer(guild_id=ctx.guild.id)
            current_locale = self._get_guild_locale(ctx.guild.id)

            if locale is None:
                message = "\n".join(
                    [
                        localizer.t(
                            "locale.server.current",
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

            if current_locale == resolved_locale:
                await ctx.send(
                    localizer.t(
                        "locale.server.already",
                        locale_name=self.localizer.locale_name(resolved_locale),
                        locale_code=resolved_locale,
                    )
                )
                return

            config = self._ensure_guild_config(ctx.guild.id)
            config["locale"] = resolved_locale
            await self.backup_data()

            new_localizer = self._get_localizer(locale=resolved_locale)
            await ctx.send(
                new_localizer.t(
                    "locale.server.changed",
                    locale_name=self.localizer.locale_name(resolved_locale),
                    locale_code=resolved_locale,
                )
            )

        @subscribe.error
        @unsubscribe.error
        @enable_updates.error
        @disable_updates.error
        @set_channel.error
        @set_thread.error
        @clear_thread.error
        @set_role.error
        @clear_role.error
        @toggle_images.error
        @settings.error
        @test_notification.error
        async def on_command_error(ctx: commands.Context, error: commands.CommandError):
            localizer = self._localizer_for_ctx(ctx)
            if isinstance(error, commands.MissingPermissions):
                await ctx.send(localizer.t("errors.permission_denied"))
                return

            if isinstance(error, commands.BadArgument):
                await ctx.send(localizer.t("errors.invalid_arguments"))
                return

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

        attachments_cache: list[AttachmentPayload] | None = None
        message_cache: dict[str, str] = {}

        async def current_attachments() -> list[AttachmentPayload]:
            nonlocal attachments_cache
            if attachments_cache is None:
                attachments_cache = await self._build_attachments(assets)
            return attachments_cache

        for config in self._enabled_guild_configs():
            guild_locale = config["locale"]
            message = message_cache.get(guild_locale)
            if message is None:
                message = self._compose_message(
                    assets,
                    self._get_localizer(locale=guild_locale),
                )
                message_cache[guild_locale] = message

            target = await self._resolve_target(config)
            if target is None:
                logger.warning(
                    f"Configured target for guild {config['guild_id']} could not be resolved."
                )
                await asyncio.sleep(self.message_delay)
                continue

            attachments = await current_attachments() if config["include_images"] else []
            try:
                await self._send_asset_message(
                    target,
                    self._compose_delivery_content(config, message),
                    attachments,
                )
                config["shown_assets"] = True
            except Exception as exc:
                logger.warning(f"Send failed for guild {config['guild_id']}: {exc}")
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
                await self._send_asset_message(
                    user_obj,
                    user_message,
                    await current_attachments(),
                )
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
                guild_configs=self.guild_configs,
                user_profiles=self.user_profiles,
                assets=self.assets_list,
                deadline=self.deadline_data,
            )
        )
        logger.info("Persisted bot state to database.")
