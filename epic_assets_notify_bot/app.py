from __future__ import annotations

import os
from pathlib import Path

from loguru import logger

from .bot import EpicAssetsNotifyBot
from .localization import Localizer

_LOGGING_CONFIGURED = False


def configure_logging(log_path: Path) -> None:
    global _LOGGING_CONFIGURED

    if _LOGGING_CONFIGURED:
        return

    logger.add(str(log_path), rotation="10 MB", level="INFO")
    _LOGGING_CONFIGURED = True


def create_bot(command_prefix: str, token: str) -> EpicAssetsNotifyBot:
    project_root = Path(__file__).resolve().parent.parent
    configure_logging(project_root / "bot.log")

    locale = os.getenv("ASSETS_BOT_LOCALE", "ru-RU")
    localizer = Localizer(
        locale=locale,
        default_locale="ru-RU",
        locales_dir=project_root / "locales",
    )

    return EpicAssetsNotifyBot(
        command_prefix=command_prefix,
        token=token,
        localizer=localizer,
    )
