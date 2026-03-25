from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from loguru import logger


class Localizer:
    def __init__(self, locale: str, default_locale: str, locales_dir: Path):
        self.locale = locale
        self.default_locale = default_locale
        self.locales_dir = locales_dir
        self._default_catalog = self._load_catalog(default_locale, required=True)
        self._catalog = (
            self._default_catalog
            if locale == default_locale
            else self._load_catalog(locale, required=False)
        )

    def translate(self, key: str, **kwargs: Any) -> str:
        template = self._lookup(self._catalog, key)
        if template is None:
            template = self._lookup(self._default_catalog, key)

        if template is None:
            logger.warning(f"Missing localization key '{key}' for locale '{self.locale}'.")
            return key

        if not isinstance(template, str):
            raise TypeError(f"Localization key '{key}' must resolve to a string.")

        return template.format(**kwargs)

    def t(self, key: str, **kwargs: Any) -> str:
        return self.translate(key, **kwargs)

    def month_name(self, month: int, context: str = "standalone") -> str:
        return self.t(f"calendar.months.{context}.wide.{month}")

    def _load_catalog(self, locale: str, required: bool) -> dict[str, Any]:
        path = self.locales_dir / f"{locale}.json"
        if not path.exists():
            if required:
                raise FileNotFoundError(f"Locale catalog not found: {path}")
            logger.warning(
                f"Locale catalog '{locale}' not found in {self.locales_dir}. Falling back to {self.default_locale}."
            )
            return {}

        with path.open("r", encoding="utf-8") as file:
            return json.load(file)

    def _lookup(self, catalog: dict[str, Any], key: str) -> Any | None:
        value: Any = catalog
        for part in key.split("."):
            if not isinstance(value, dict) or part not in value:
                return None
            value = value[part]
        return value
