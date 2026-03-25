from __future__ import annotations

import os
from pathlib import Path

from .database import DatabaseSnapshot, sqlite_url_from_path
from .state import StateNormalizer
from .storage import load_json_if_exists


def get_data_folder() -> Path:
    raw_path = os.getenv("ASSETS_BOT_DATA_DIR")
    if raw_path:
        return Path(raw_path).expanduser()

    return Path("/data") if os.name != "nt" else Path("data")


def get_database_url(data_folder: Path) -> str:
    database_url = os.getenv("ASSETS_BOT_DATABASE_URL")
    if database_url:
        return database_url

    return sqlite_url_from_path(data_folder / "bot.db")


def load_legacy_snapshot(data_folder: Path, normalizer: StateNormalizer) -> DatabaseSnapshot:
    channels_path = data_folder / "subscribers_channels_backup.json"
    users_path = data_folder / "subscribers_users_backup.json"
    assets_path = data_folder / "assets_backup.json"
    deadline_path = data_folder / "deadline_backup.json"

    legacy_paths = [channels_path, users_path, assets_path, deadline_path]
    if not any(path.exists() for path in legacy_paths):
        return DatabaseSnapshot(channels=[], user_profiles=[], assets=[], deadline=None)

    return DatabaseSnapshot(
        channels=normalizer.normalize_channels(load_json_if_exists(channels_path, [])),
        user_profiles=normalizer.normalize_user_profiles(load_json_if_exists(users_path, [])),
        assets=normalizer.normalize_assets(load_json_if_exists(assets_path, [])),
        deadline=normalizer.normalize_deadline(load_json_if_exists(deadline_path, "")),
    )
