from __future__ import annotations

import json
from pathlib import Path
from typing import Any, TypeVar

from loguru import logger

T = TypeVar("T")


def ensure_directory(path: Path) -> None:
    path.mkdir(parents=True, exist_ok=True)


def load_json(path: Path, default: T) -> T:
    if not path.exists():
        logger.warning(f"{path.name} not found. Load failed.")
        return default

    try:
        with path.open("r", encoding="utf-8") as file:
            data = json.load(file)
    except Exception as exc:
        logger.error(f"Failed to load {path.name}: {exc}")
        return default

    logger.info(f"Loaded {_count_label(data)} objects from {path.name}.")
    return data


def save_json(path: Path, payload: Any, log_message: str) -> None:
    ensure_directory(path.parent)
    with path.open("w", encoding="utf-8") as file:
        json.dump(payload, file)
    logger.info(log_message)


def _count_label(payload: Any) -> str:
    if isinstance(payload, (list, tuple, set, dict)):
        return str(len(payload))
    return "1"