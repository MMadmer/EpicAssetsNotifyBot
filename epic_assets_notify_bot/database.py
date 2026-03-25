from __future__ import annotations

import asyncio
import json
from dataclasses import dataclass
from pathlib import Path

from loguru import logger
from sqlalchemy import BigInteger, Boolean, Integer, String, Text, delete, event, select
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker, create_async_engine
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column

from .scraper import Asset
from .state import ChannelSubscription, GuildConfig, StoredDeadline, UserProfile

DEADLINE_STATE_KEY = "deadline"


class Base(DeclarativeBase):
    pass


class ChannelSubscriptionRecord(Base):
    __tablename__ = "channel_subscriptions"

    channel_id: Mapped[int] = mapped_column(BigInteger, primary_key=True)
    shown_assets: Mapped[bool] = mapped_column(Boolean, nullable=False, default=False)
    locale: Mapped[str] = mapped_column(String(32), nullable=False)


class GuildConfigRecord(Base):
    __tablename__ = "guild_configs"

    guild_id: Mapped[int] = mapped_column(BigInteger, primary_key=True)
    channel_id: Mapped[int | None] = mapped_column(BigInteger, nullable=True)
    thread_id: Mapped[int | None] = mapped_column(BigInteger, nullable=True)
    shown_assets: Mapped[bool] = mapped_column(Boolean, nullable=False, default=False)
    locale: Mapped[str] = mapped_column(String(32), nullable=False)
    mention_role_id: Mapped[int | None] = mapped_column(BigInteger, nullable=True)
    enabled: Mapped[bool] = mapped_column(Boolean, nullable=False, default=True)
    include_images: Mapped[bool] = mapped_column(Boolean, nullable=False, default=True)


class UserProfileRecord(Base):
    __tablename__ = "user_profiles"

    user_id: Mapped[int] = mapped_column(BigInteger, primary_key=True)
    shown_assets: Mapped[bool] = mapped_column(Boolean, nullable=False, default=False)
    locale: Mapped[str] = mapped_column(String(32), nullable=False)
    subscribed: Mapped[bool] = mapped_column(Boolean, nullable=False, default=True)


class CurrentAssetRecord(Base):
    __tablename__ = "current_assets"

    position: Mapped[int] = mapped_column(Integer, primary_key=True)
    name: Mapped[str | None] = mapped_column(Text, nullable=True)
    link: Mapped[str] = mapped_column(Text, nullable=False, unique=True)
    image: Mapped[str | None] = mapped_column(Text, nullable=True)


class BotStateRecord(Base):
    __tablename__ = "bot_state"

    key: Mapped[str] = mapped_column(String(64), primary_key=True)
    value: Mapped[str] = mapped_column(Text, nullable=False)


@dataclass(slots=True)
class DatabaseSnapshot:
    guild_configs: list[GuildConfig]
    user_profiles: list[UserProfile]
    assets: list[Asset]
    deadline: StoredDeadline


@dataclass(slots=True)
class LegacyDatabaseSnapshot:
    channels: list[ChannelSubscription]
    user_profiles: list[UserProfile]
    assets: list[Asset]
    deadline: StoredDeadline


def sqlite_url_from_path(path: Path) -> str:
    return f"sqlite+aiosqlite:///{path.resolve().as_posix()}"


class DatabaseManager:
    def __init__(self, database_url: str):
        self.database_url = database_url
        self._is_sqlite = database_url.startswith("sqlite")
        connect_args = {"timeout": 30} if self._is_sqlite else {}
        self.engine = create_async_engine(database_url, connect_args=connect_args)
        self.session_factory = async_sessionmaker(self.engine, expire_on_commit=False)
        self._save_lock = asyncio.Lock()

        if self._is_sqlite:
            self._configure_sqlite()

    def _configure_sqlite(self) -> None:
        @event.listens_for(self.engine.sync_engine, "connect")
        def configure_sqlite(dbapi_connection, _connection_record) -> None:
            cursor = dbapi_connection.cursor()
            try:
                cursor.execute("PRAGMA foreign_keys=ON")
                cursor.execute("PRAGMA busy_timeout=5000")
                cursor.execute("PRAGMA journal_mode=WAL")
            finally:
                cursor.close()

    async def initialize(self) -> None:
        async with self.engine.begin() as connection:
            await connection.run_sync(Base.metadata.create_all)

    async def dispose(self) -> None:
        await self.engine.dispose()

    async def load_snapshot(self) -> DatabaseSnapshot:
        async with self.session_factory() as session:
            guild_config_records = (
                await session.scalars(select(GuildConfigRecord).order_by(GuildConfigRecord.guild_id))
            ).all()
            user_records = (
                await session.scalars(select(UserProfileRecord).order_by(UserProfileRecord.user_id))
            ).all()
            asset_records = (
                await session.scalars(select(CurrentAssetRecord).order_by(CurrentAssetRecord.position))
            ).all()
            deadline = await self._load_deadline(session)

        return DatabaseSnapshot(
            guild_configs=[
                {
                    "guild_id": record.guild_id,
                    "channel_id": record.channel_id,
                    "thread_id": record.thread_id,
                    "shown_assets": record.shown_assets,
                    "locale": record.locale,
                    "mention_role_id": record.mention_role_id,
                    "enabled": record.enabled,
                    "include_images": record.include_images,
                }
                for record in guild_config_records
            ],
            user_profiles=[
                {
                    "id": record.user_id,
                    "shown_assets": record.shown_assets,
                    "locale": record.locale,
                    "subscribed": record.subscribed,
                }
                for record in user_records
            ],
            assets=[
                {
                    "name": record.name,
                    "link": record.link,
                    "image": record.image,
                }
                for record in asset_records
            ],
            deadline=deadline,
        )

    async def load_legacy_channel_subscriptions(self) -> list[ChannelSubscription]:
        async with self.session_factory() as session:
            channel_records = (
                await session.scalars(
                    select(ChannelSubscriptionRecord).order_by(ChannelSubscriptionRecord.channel_id)
                )
            ).all()

        return [
            {
                "id": record.channel_id,
                "shown_assets": record.shown_assets,
                "locale": record.locale,
            }
            for record in channel_records
        ]

    async def save_snapshot(self, snapshot: DatabaseSnapshot) -> None:
        async with self._save_lock:
            async with self.session_factory.begin() as session:
                await self._replace_runtime_snapshot(session, snapshot)

    async def import_legacy_snapshot_if_empty(self, snapshot: LegacyDatabaseSnapshot) -> bool:
        if not self._legacy_snapshot_has_content(snapshot):
            return False

        async with self._save_lock:
            async with self.session_factory.begin() as session:
                if await self._has_any_state(session):
                    return False

                await self._replace_legacy_snapshot(session, snapshot)
                logger.info("Imported legacy JSON state into database.")
                return True

    async def _replace_runtime_snapshot(
        self,
        session: AsyncSession,
        snapshot: DatabaseSnapshot,
    ) -> None:
        await session.execute(delete(GuildConfigRecord))
        await session.execute(delete(UserProfileRecord))
        await session.execute(delete(CurrentAssetRecord))

        session.add_all(
            [
                GuildConfigRecord(
                    guild_id=config["guild_id"],
                    channel_id=config["channel_id"],
                    thread_id=config["thread_id"],
                    shown_assets=config["shown_assets"],
                    locale=config["locale"],
                    mention_role_id=config["mention_role_id"],
                    enabled=config["enabled"],
                    include_images=config["include_images"],
                )
                for config in snapshot.guild_configs
            ]
        )
        session.add_all(
            [
                UserProfileRecord(
                    user_id=user_profile["id"],
                    shown_assets=user_profile["shown_assets"],
                    locale=user_profile["locale"],
                    subscribed=user_profile["subscribed"],
                )
                for user_profile in snapshot.user_profiles
            ]
        )
        session.add_all(
            [
                CurrentAssetRecord(
                    position=index,
                    name=asset.get("name"),
                    link=asset["link"],
                    image=asset.get("image"),
                )
                for index, asset in enumerate(snapshot.assets, start=1)
                if asset.get("link")
            ]
        )

        await self._store_deadline(session, snapshot.deadline)

    async def _replace_legacy_snapshot(
        self,
        session: AsyncSession,
        snapshot: LegacyDatabaseSnapshot,
    ) -> None:
        await session.execute(delete(ChannelSubscriptionRecord))
        await session.execute(delete(GuildConfigRecord))
        await session.execute(delete(UserProfileRecord))
        await session.execute(delete(CurrentAssetRecord))

        session.add_all(
            [
                ChannelSubscriptionRecord(
                    channel_id=channel["id"],
                    shown_assets=channel["shown_assets"],
                    locale=channel["locale"],
                )
                for channel in snapshot.channels
            ]
        )
        session.add_all(
            [
                UserProfileRecord(
                    user_id=user_profile["id"],
                    shown_assets=user_profile["shown_assets"],
                    locale=user_profile["locale"],
                    subscribed=user_profile["subscribed"],
                )
                for user_profile in snapshot.user_profiles
            ]
        )
        session.add_all(
            [
                CurrentAssetRecord(
                    position=index,
                    name=asset.get("name"),
                    link=asset["link"],
                    image=asset.get("image"),
                )
                for index, asset in enumerate(snapshot.assets, start=1)
                if asset.get("link")
            ]
        )

        await self._store_deadline(session, snapshot.deadline)

    async def _store_deadline(self, session: AsyncSession, deadline: StoredDeadline) -> None:
        deadline_record = await session.get(BotStateRecord, DEADLINE_STATE_KEY)
        encoded_deadline = json.dumps(deadline)
        if deadline_record is None:
            session.add(BotStateRecord(key=DEADLINE_STATE_KEY, value=encoded_deadline))
            return

        deadline_record.value = encoded_deadline

    async def _load_deadline(self, session: AsyncSession) -> StoredDeadline:
        deadline_record = await session.get(BotStateRecord, DEADLINE_STATE_KEY)
        if deadline_record is None:
            return None

        try:
            payload = json.loads(deadline_record.value)
        except json.JSONDecodeError as exc:
            logger.warning(f"Failed to decode deadline payload from database: {exc}")
            return None

        if payload is None or isinstance(payload, (dict, str)):
            return payload

        logger.warning("Unexpected deadline payload type in database. Resetting to empty state.")
        return None

    async def _has_any_state(self, session: AsyncSession) -> bool:
        if await session.scalar(select(ChannelSubscriptionRecord.channel_id).limit(1)) is not None:
            return True

        if await session.scalar(select(GuildConfigRecord.guild_id).limit(1)) is not None:
            return True

        if await session.scalar(select(UserProfileRecord.user_id).limit(1)) is not None:
            return True

        if await session.scalar(select(CurrentAssetRecord.position).limit(1)) is not None:
            return True

        deadline_record = await session.get(BotStateRecord, DEADLINE_STATE_KEY)
        if deadline_record is not None and deadline_record.value != "null":
            return True

        return False

    def _legacy_snapshot_has_content(self, snapshot: LegacyDatabaseSnapshot) -> bool:
        return bool(
            snapshot.channels
            or snapshot.user_profiles
            or snapshot.assets
            or snapshot.deadline is not None
        )
