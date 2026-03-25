from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from loguru import logger


class Localizer:
    def __init__(
        self,
        locale: str,
        default_locale: str,
        locales_dir: Path,
        catalog_cache: dict[str, dict[str, Any]] | None = None,
        locale_name_cache: dict[str, str] | None = None,
    ):
        self.locale = locale
        self.default_locale = default_locale
        self.locales_dir = locales_dir
        self._catalog_cache = catalog_cache if catalog_cache is not None else {}
        self._locale_name_cache = locale_name_cache if locale_name_cache is not None else {}
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

    def for_locale(self, locale: str | None) -> Localizer:
        resolved_locale = self.normalize_locale(locale) or self.default_locale
        if resolved_locale == self.locale:
            return self

        return Localizer(
            locale=resolved_locale,
            default_locale=self.default_locale,
            locales_dir=self.locales_dir,
            catalog_cache=self._catalog_cache,
            locale_name_cache=self._locale_name_cache,
        )

    def available_locales(self) -> dict[str, str]:
        locales: dict[str, str] = {}
        for path in sorted(self.locales_dir.glob("*.json")):
            locale_code = path.stem
            locales[locale_code] = self._catalog_locale_name(locale_code)
        return locales

    def locale_name(self, locale: str) -> str:
        resolved_locale = self.normalize_locale(locale) or locale.strip().replace("_", "-")
        return self._catalog_locale_name(resolved_locale)

    def normalize_locale(self, locale: str | None) -> str | None:
        if locale is None:
            return None

        candidate = locale.strip().replace("_", "-")
        if not candidate:
            return None

        normalized_candidate = candidate.casefold()
        aliases: dict[str, str] = {}

        for locale_code, locale_name in self.available_locales().items():
            aliases[locale_code.casefold()] = locale_code
            aliases[locale_code.replace("-", "").casefold()] = locale_code
            aliases.setdefault(locale_code.split("-")[0].casefold(), locale_code)

            normalized_name = locale_name.strip().casefold()
            if normalized_name:
                aliases[normalized_name] = locale_code

        return aliases.get(normalized_candidate)

    def _catalog_locale_name(self, locale: str) -> str:
        if locale in self._locale_name_cache:
            return self._locale_name_cache[locale]

        catalog = self._load_catalog(locale, required=False)
        name = locale
        if isinstance(catalog, dict):
            meta = catalog.get("meta")
            if isinstance(meta, dict):
                localized_name = meta.get("name")
                if isinstance(localized_name, str) and localized_name.strip():
                    name = localized_name.strip()

        self._locale_name_cache[locale] = name
        return name

    def _load_catalog(self, locale: str, required: bool) -> dict[str, Any]:
        if locale in self._catalog_cache:
            return self._catalog_cache[locale]

        path = self.locales_dir / f"{locale}.json"
        if not path.exists():
            if required:
                raise FileNotFoundError(f"Locale catalog not found: {path}")
            logger.warning(
                f"Locale catalog '{locale}' not found in {self.locales_dir}. Falling back to {self.default_locale}."
            )
            return {}

        # Accept BOM-prefixed UTF-8 locale catalogs so deployments don't fail on
        # locale files rewritten by Windows tooling.
        with path.open("r", encoding="utf-8-sig") as file:
            catalog = json.load(file)

        if not isinstance(catalog, dict):
            raise TypeError(f"Locale catalog '{locale}' must contain a JSON object.")

        self._catalog_cache[locale] = catalog
        return catalog

    def _lookup(self, catalog: dict[str, Any], key: str) -> Any | None:
        value: Any = catalog
        for part in key.split("."):
            if not isinstance(value, dict) or part not in value:
                return None
            value = value[part]
        return value
