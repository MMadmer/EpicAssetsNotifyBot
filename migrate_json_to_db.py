from __future__ import annotations

import asyncio
import os
from pathlib import Path

from loguru import logger

from epic_assets_notify_bot.config import get_data_folder, get_database_url, load_legacy_snapshot
from epic_assets_notify_bot.database import DatabaseManager
from epic_assets_notify_bot.localization import Localizer
from epic_assets_notify_bot.state import StateNormalizer
from epic_assets_notify_bot.storage import ensure_directory


async def migrate() -> None:
    project_root = Path(__file__).resolve().parent
    data_folder = get_data_folder()
    database_url = get_database_url(data_folder)

    ensure_directory(data_folder)

    localizer = Localizer(
        locale=os.getenv("ASSETS_BOT_LOCALE", "ru-RU"),
        default_locale="ru-RU",
        locales_dir=project_root / "locales",
    )
    base_locale = localizer.normalize_locale(localizer.locale) or localizer.default_locale
    normalizer = StateNormalizer(localizer=localizer, base_locale=base_locale)
    legacy_snapshot = load_legacy_snapshot(data_folder, normalizer)

    database = DatabaseManager(database_url)
    try:
        await database.initialize()
        migrated = await database.import_legacy_snapshot_if_empty(legacy_snapshot)

        if not migrated:
            logger.info(
                "Migration skipped. The database already contains data or no legacy JSON files were found."
            )
            return

        channels = await database.load_legacy_channel_subscriptions()
        snapshot = await database.load_snapshot()
        logger.info(
            "Migration completed successfully: "
            f"{len(channels)} legacy channels, "
            f"{len(snapshot.user_profiles)} user profiles, "
            f"{len(snapshot.assets)} assets."
        )
    finally:
        await database.dispose()


if __name__ == "__main__":
    asyncio.run(migrate())
