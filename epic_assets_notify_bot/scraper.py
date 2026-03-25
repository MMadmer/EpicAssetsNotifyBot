from __future__ import annotations

import asyncio
import os
import random
import re
from datetime import datetime, timedelta
from typing import TypedDict
from zoneinfo import ZoneInfo

from bs4 import BeautifulSoup
from loguru import logger
from playwright.async_api import async_playwright
from pyvirtualdisplay import Display

Asset = dict[str, str | None]


class DeadlineInfo(TypedDict):
    day: int
    month: int
    year: int
    hour: int
    minute: int
    gmt_offset: str


def _clean_text(text: str) -> str:
    return re.sub(r"\s+", " ", text or "").strip()


def _parse_deadline_info(heading_text: str) -> DeadlineInfo | None:
    """
    Parse strings like:
      "Limited-Time Free (Until Sept 9 at 9:59 AM ET)"
      "Limited-Time Free (Until Sep 9, 2025 at 9:59 AM ET)"
    """
    if not heading_text:
        return None

    match = re.search(r"\(([^)]*Until[^)]*)\)", heading_text, flags=re.IGNORECASE)
    if not match:
        return None
    inside = match.group(1)

    pattern = re.compile(
        r"Until\s+([A-Za-z]{3,9})\s+(\d{1,2})(?:,?\s*(\d{4}))?\s+at\s+(\d{1,2}):(\d{2})\s*(AM|PM)?\s*([A-Z]{2,4})",
        re.IGNORECASE,
    )
    match = pattern.search(inside)
    if not match:
        return None

    month_name_en = match.group(1).lower()
    day = int(match.group(2))
    year = int(match.group(3)) if match.group(3) else datetime.now().year
    hour_12 = int(match.group(4))
    minute = int(match.group(5))
    meridiem = (match.group(6) or "").upper()
    timezone_abbr = (match.group(7) or "").upper()

    month_map = {
        "jan": 1,
        "january": 1,
        "feb": 2,
        "february": 2,
        "mar": 3,
        "march": 3,
        "apr": 4,
        "april": 4,
        "may": 5,
        "jun": 6,
        "june": 6,
        "jul": 7,
        "july": 7,
        "aug": 8,
        "august": 8,
        "sep": 9,
        "sept": 9,
        "september": 9,
        "oct": 10,
        "october": 10,
        "nov": 11,
        "november": 11,
        "dec": 12,
        "december": 12,
    }
    month = month_map.get(month_name_en)
    if not month:
        return None

    hour = hour_12 % 12
    if meridiem == "PM":
        hour += 12

    timezone_map = {
        "ET": "America/New_York",
        "PT": "America/Los_Angeles",
        "UTC": "UTC",
        "GMT": "UTC",
    }
    timezone_name = timezone_map.get(timezone_abbr, "UTC")
    try:
        timezone = ZoneInfo(timezone_name)
    except Exception:
        timezone = ZoneInfo("UTC")

    local_dt = datetime(year, month, day, hour, minute, tzinfo=timezone)
    offset = local_dt.utcoffset() or timedelta(0)
    total_minutes = int(offset.total_seconds() // 60)
    sign = "+" if total_minutes >= 0 else "-"
    total_minutes = abs(total_minutes)
    offset_hours, offset_minutes = divmod(total_minutes, 60)
    gmt_offset = (
        f"GMT{sign}{offset_hours}"
        if offset_minutes == 0
        else f"GMT{sign}{offset_hours}:{offset_minutes:02d}"
    )

    return {
        "day": day,
        "month": month,
        "year": year,
        "hour": hour,
        "minute": minute,
        "gmt_offset": gmt_offset,
    }


def _normalize_image_url(url: str | None) -> str | None:
    if not url:
        return None
    if url.startswith("//"):
        return "https:" + url
    if url.startswith("/"):
        return "https://www.fab.com" + url
    return url


async def get_free_assets(retries: int = 5) -> tuple[list[Asset] | None, DeadlineInfo | None]:
    """
    Go to Fab homepage, find 'Limited-Time Free' section, and collect listing cards directly
    from the homepage (no per-listing navigation).
    Returns: (assets, deadline_info)
    """
    homepage_url = "https://www.fab.com/"
    display = Display() if not os.getenv("DISPLAY") else None

    if display:
        display.start()

    try:
        for attempt in range(1, retries + 1):
            browser = None
            try:
                async with async_playwright() as playwright:
                    browser = await playwright.firefox.launch(
                        headless=True,
                        args=["--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage"],
                    )
                    page = await browser.new_page()

                    logger.info("Loading homepage...")
                    await page.goto(homepage_url, wait_until="domcontentloaded", timeout=60000)
                    await asyncio.sleep(1.0)

                    content = await page.content()
                    soup = BeautifulSoup(content, "html.parser")

                    free_section = None
                    heading_text = None
                    for heading in soup.find_all("h2"):
                        normalized_heading = _clean_text(heading.get_text(" ", strip=True))
                        if normalized_heading.startswith("Limited-Time Free"):
                            free_section = heading.find_parent("section")
                            heading_text = normalized_heading
                            break

                    if not free_section:
                        logger.warning(
                            f"'Limited-Time Free' section not found. Attempt {attempt}/{retries}"
                        )
                        await asyncio.sleep(random.uniform(5, 9))
                        continue

                    deadline_info = _parse_deadline_info(heading_text) if heading_text else None

                    items: list[Asset] = []
                    seen_links: set[str] = set()
                    for listing in free_section.find_all("li"):
                        link_tag = listing.find(
                            "a",
                            href=lambda href: href and href.startswith("/listings/"),
                        )
                        if not link_tag:
                            continue

                        link = "https://www.fab.com" + link_tag["href"]
                        if link in seen_links:
                            continue
                        seen_links.add(link)

                        name = _clean_text(link_tag.get_text(" ", strip=True))
                        if not name:
                            aria_label = link_tag.get("aria-label")
                            if aria_label:
                                name = _clean_text(aria_label)

                        image_tag = listing.find("img")
                        image_url = image_tag["src"] if image_tag and image_tag.get("src") else None

                        items.append(
                            {
                                "name": name or None,
                                "link": link,
                                "image": _normalize_image_url(image_url),
                            }
                        )

                    if not items:
                        logger.info("Limited-Time Free section is empty on homepage.")
                        return [], deadline_info

                    logger.info(f"Collected {len(items)} listing cards from homepage.")
                    return items, deadline_info

            except Exception as exc:
                logger.error(f"Homepage parse error: {exc}. Retrying {attempt}/{retries}...")
                await asyncio.sleep(random.uniform(10, 15))
            finally:
                if browser:
                    await browser.close()

        logger.error("Failed to fetch Limited-Time Free assets after several attempts.")
        return None, None

    finally:
        if display:
            display.stop()
