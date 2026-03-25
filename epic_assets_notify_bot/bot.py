from __future__ import annotations

import asyncio
import os
import re
from dataclasses import dataclass
from datetime import datetime, timedelta
from io import BytesIO
from pathlib import Path

import aiohttp
import discord
from discord.ext import commands
from loguru import logger

from .localization import Localizer
from .scraper import Asset, get_free_assets
from .storage import ensure_directory, load_json, save_json


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
        self.data_folder = Path("/data") if os.name != "nt" else Path("data")
        self.channels_backup_path = self.data_folder / "subscribers_channels_backup.json"
        self.users_backup_path = self.data_folder / "subscribers_users_backup.json"
        self.assets_backup_path = self.data_folder / "assets_backup.json"
        self.deadline_backup_path = self.data_folder / "deadline_backup.json"

        self.subscribed_channels = load_json(self.channels_backup_path, [])
        self.subscribed_users = load_json(self.users_backup_path, [])
        self.assets_list = load_json(self.assets_backup_path, [])
        self.deadline_suffix = load_json(self.deadline_backup_path, "")
        if not isinstance(self.deadline_suffix, str):
            self.deadline_suffix = ""

        self.next_check_time = None
        self.delete_after = 10
        self.backup_delay = 900
        self.message_delay = 0.5

        self.add_commands()

    async def on_ready(self):
        logger.info(f"Logged in as {self.user}")
        self.loop.create_task(self.set_daily_check())
        self.loop.create_task(self.backup_loop())

    def run_bot(self):
        if not self.data_folder.exists():
            logger.info(f"Creating data folder at {self.data_folder}")
        ensure_directory(self.data_folder)

        logger.info("Starting bot...")
        self.run(self.token)

    def _compose_header(self) -> str:
        month_name = self.localizer.month_name(datetime.now().month, context="standalone")
        if self.deadline_suffix:
            return self.localizer.t(
                "header.with_deadline",
                month_name=month_name,
                deadline_suffix=self.deadline_suffix,
            )
        return self.localizer.t("header.without_deadline", month_name=month_name)

    async def _build_message_and_attachments(
        self, assets: list[Asset]
    ) -> tuple[str, list[AttachmentPayload]]:
        message = self._compose_header()
        attachments: list[AttachmentPayload] = []

        async with aiohttp.ClientSession() as session:
            for asset in assets:
                message += f"- [{asset['name']}](<{asset['link']}>)\n"
                image_url = asset.get("image")
                if not image_url:
                    continue

                try:
                    async with session.get(image_url, timeout=30) as response:
                        image_data = await response.read()
                    safe_name = re.sub(r'[\\/*?:"<>|]+', "_", asset["name"])[:100] or "image"
                    attachments.append(
                        AttachmentPayload(filename=f"{safe_name}.png", content=image_data)
                    )
                except Exception as exc:
                    logger.warning(f"Image fetch failed for {asset['link']}: {exc}")

        return message, attachments

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

    def add_commands(self):
        @self.command(name="sub")
        async def subscribe(ctx: commands.Context):
            permission_denied = self.localizer.t("errors.permission_denied")
            if not is_admin(ctx) and not is_dm(ctx):
                await ctx.send(permission_denied)
                return

            if is_dm(ctx):
                user_id = ctx.author.id
                if any(user["id"] == user_id for user in self.subscribed_users):
                    await ctx.send(self.localizer.t("subscribe.dm.already"))
                    return

                self.subscribed_users.append({"id": user_id, "shown_assets": False})
                await ctx.send(self.localizer.t("subscribe.dm.success"))
                logger.info(f"User {ctx.author} subscribed to asset updates.")

                if self.assets_list and not self.subscribed_users[-1]["shown_assets"]:
                    message, attachments = await self._build_message_and_attachments(self.assets_list)
                    user = await self.fetch_user(user_id)
                    await self._send_asset_message(user, message, attachments)
                    self.subscribed_users[-1]["shown_assets"] = True
                return

            channel_id = ctx.channel.id
            if any(channel["id"] == channel_id for channel in self.subscribed_channels):
                await ctx.send(self.localizer.t("subscribe.channel.already"))
                return

            self.subscribed_channels.append({"id": channel_id, "shown_assets": False})
            await ctx.send(
                self.localizer.t("subscribe.channel.success", channel_name=ctx.channel.name)
            )
            logger.info(f"Channel {ctx.channel.name} subscribed to asset updates.")

            if self.assets_list and not self.subscribed_channels[-1]["shown_assets"]:
                message, attachments = await self._build_message_and_attachments(self.assets_list)
                channel = self.get_channel(channel_id)
                if channel:
                    await self._send_asset_message(channel, message, attachments)
                    self.subscribed_channels[-1]["shown_assets"] = True

        @self.command(name="unsub")
        async def unsubscribe(ctx: commands.Context):
            permission_denied = self.localizer.t("errors.permission_denied")
            if not is_admin(ctx) and not is_dm(ctx):
                await ctx.send(permission_denied)
                return

            if is_dm(ctx):
                user_id = ctx.author.id
                for user in self.subscribed_users:
                    if user["id"] == user_id:
                        self.subscribed_users.remove(user)
                        await ctx.send(self.localizer.t("unsubscribe.success"))
                        logger.info(f"User {ctx.author} unsubscribed from asset updates.")
                        return

                await ctx.send(self.localizer.t("unsubscribe.dm.not_subscribed"))
                return

            channel_id = ctx.channel.id
            for channel in self.subscribed_channels:
                if channel["id"] == channel_id:
                    self.subscribed_channels.remove(channel)
                    await ctx.send(self.localizer.t("unsubscribe.success"))
                    logger.info(f"Channel {ctx.channel.name} unsubscribed from asset updates.")
                    return

            await ctx.send(self.localizer.t("unsubscribe.channel.not_subscribed"))

        @self.command(name="time")
        async def time_left(ctx: commands.Context):
            delete_hint = self.localizer.t("time.delete_hint", delete_after=self.delete_after)

            if self.next_check_time:
                now = datetime.now()
                time_remaining = self.next_check_time - now
                hours, remainder = divmod(time_remaining.seconds, 3600)
                minutes, seconds = divmod(remainder, 60)
                message = "\n".join(
                    [
                        self.localizer.t(
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

            message = "\n".join([self.localizer.t("time.no_schedule"), delete_hint])
            await self._send_temporary_message(ctx, message)

        @subscribe.error
        @unsubscribe.error
        async def on_command_error(ctx: commands.Context, error: commands.CommandError):
            if isinstance(error, commands.MissingPermissions):
                await ctx.send(self.localizer.t("errors.permission_denied"))

    async def set_daily_check(self):
        while True:
            self.next_check_time = datetime.now() + timedelta(days=1)
            await self.check_and_notify_assets()
            await asyncio.sleep(24 * 60 * 60)

    async def check_and_notify_assets(self):
        assets, deadline = await get_free_assets(self.localizer)
        if not assets:
            return

        def asset_ids(items: list[Asset]) -> set[str]:
            return {asset["link"] for asset in items or []}

        new_ids = asset_ids(assets)
        old_ids = asset_ids(self.assets_list)
        deadline = deadline or ""
        deadline_changed = deadline != (self.deadline_suffix or "")

        added = new_ids - old_ids

        if not deadline_changed and not added and new_ids.issubset(old_ids) and new_ids != old_ids:
            logger.warning("Shrink-only change detected — likely transient scrape issue. Skipping update.")
            return

        if not deadline_changed and new_ids == old_ids:
            return

        self.assets_list = assets
        self.deadline_suffix = deadline

        message, attachments = await self._build_message_and_attachments(assets)

        for channel in self.subscribed_channels:
            channel_obj = self.get_channel(channel["id"])
            if channel_obj:
                await self._send_asset_message(channel_obj, message, attachments)
                channel["shown_assets"] = True
            await asyncio.sleep(self.message_delay)

        for user in self.subscribed_users:
            user_obj = await self.fetch_user(user["id"])
            if user_obj:
                try:
                    await self._send_asset_message(user_obj, message, attachments)
                    user["shown_assets"] = True
                except Exception as exc:
                    logger.warning(f"DM failed for {user['id']}: {exc}")
            await asyncio.sleep(self.message_delay)

        await self.backup_data()

    async def backup_loop(self):
        while True:
            await self.backup_data()
            await asyncio.sleep(self.backup_delay)

    async def backup_data(self):
        save_json(
            self.channels_backup_path,
            self.subscribed_channels,
            f"Saved {len(self.subscribed_channels)} subscribed channels to backup.",
        )
        save_json(
            self.users_backup_path,
            self.subscribed_users,
            f"Saved {len(self.subscribed_users)} subscribed users to backup.",
        )
        save_json(
            self.assets_backup_path,
            self.assets_list,
            f"Saved {len(self.assets_list) if self.assets_list else 0} assets to backup.",
        )
        save_json(self.deadline_backup_path, self.deadline_suffix, "Saved deadline suffix to backup.")