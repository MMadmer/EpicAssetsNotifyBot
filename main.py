import random
import aiohttp
import discord
from discord.ext import commands
from playwright.async_api import async_playwright
from bs4 import BeautifulSoup
import asyncio
from datetime import datetime, timedelta
from io import BytesIO
import json
import os
import re
from pyvirtualdisplay import Display
from loguru import logger
from zoneinfo import ZoneInfo

logger.add("bot.log", rotation="10 MB", level="INFO")


def get_month_name():
    month_names = {
        1: "Январские", 2: "Февральские", 3: "Мартовские", 4: "Апрельские",
        5: "Майские", 6: "Июньские", 7: "Июльские", 8: "Августовские",
        9: "Сентябрьские", 10: "Октябрьские", 11: "Ноябрьские", 12: "Декабрьские"
    }
    current_month = datetime.now().month
    return month_names[current_month]


def _clean_text(s: str) -> str:
    return re.sub(r"\s+", " ", s or "").strip()


def _rus_month_genitive(month_num: int) -> str:
    gen = {
        1: "января", 2: "февраля", 3: "марта", 4: "апреля",
        5: "мая", 6: "июня", 7: "июля", 8: "августа",
        9: "сентября", 10: "октября", 11: "ноября", 12: "декабря"
    }
    return gen.get(month_num, "")


def _parse_deadline_suffix(heading_text: str) -> str | None:
    """
    Parse strings like:
      "Limited-Time Free (Until Sept 9 at 9:59 AM ET)"
      "Limited-Time Free (Until Sep 9, 2025 at 9:59 AM ET)"
    Return (ru): "до 9 сентября 9:59 GMT-4"
    """
    if not heading_text:
        return None

    # Extract "(Until ...)" part
    m_paren = re.search(r"\(([^)]*Until[^)]*)\)", heading_text, flags=re.IGNORECASE)
    if not m_paren:
        return None
    inside = m_paren.group(1)

    # Capture: Month Day [Year] at HH:MM AM/PM TZ
    rx = re.compile(
        r"Until\s+([A-Za-z]{3,9})\s+(\d{1,2})(?:,?\s*(\d{4}))?\s+at\s+(\d{1,2}):(\d{2})\s*(AM|PM)?\s*([A-Z]{2,4})",
        re.IGNORECASE
    )
    m = rx.search(inside)
    if not m:
        return None

    mon_name_en = m.group(1).lower()
    day = int(m.group(2))
    year = int(m.group(3)) if m.group(3) else datetime.now().year
    hh12 = int(m.group(4))
    mm = int(m.group(5))
    ampm = (m.group(6) or "").upper()
    tz_abbr = (m.group(7) or "").upper()

    mon_map = {
        "jan": 1, "january": 1,
        "feb": 2, "february": 2,
        "mar": 3, "march": 3,
        "apr": 4, "april": 4,
        "may": 5,
        "jun": 6, "june": 6,
        "jul": 7, "july": 7,
        "aug": 8, "august": 8,
        "sep": 9, "sept": 9, "september": 9,
        "oct": 10, "october": 10,
        "nov": 11, "november": 11,
        "dec": 12, "december": 12,
    }
    month = mon_map.get(mon_name_en)
    if not month:
        return None

    # 12h → 24h
    hour = hh12 % 12
    if ampm == "PM":
        hour += 12

    # TZ map (Fab typically shows ET)
    tz_map = {
        "ET": "America/New_York",
        "PT": "America/Los_Angeles",
        "UTC": "UTC",
        "GMT": "UTC"
    }
    tz_name = tz_map.get(tz_abbr, "UTC")
    try:
        tz = ZoneInfo(tz_name)
    except Exception:
        tz = ZoneInfo("UTC")

    local_dt = datetime(year, month, day, hour, mm, tzinfo=tz)

    # "GMT±H[:MM]"
    offset = local_dt.utcoffset() or timedelta(0)
    total_minutes = int(offset.total_seconds() // 60)
    sign = "+" if total_minutes >= 0 else "-"
    total_minutes = abs(total_minutes)
    off_h, off_m = divmod(total_minutes, 60)
    gmt_str = f"GMT{sign}{off_h}" if off_m == 0 else f"GMT{sign}{off_h}:{off_m:02d}"

    rus_month = _rus_month_genitive(month)
    return f"до {day} {rus_month} {hour}:{mm:02d} {gmt_str}"


async def get_free_assets(retries: int = 5):
    """
    Go to Fab homepage, find 'Limited-Time Free' section, collect listing cards directly
    from the homepage (no per-listing navigation).
    Returns: (assets: list[dict{name, link, image}], deadline_suffix: str|None)
    """
    homepage_url = "https://www.fab.com/"
    display = Display() if not os.getenv("DISPLAY") else None

    if display:
        display.start()

    try:
        for attempt in range(1, retries + 1):
            try:
                async with async_playwright() as p:
                    browser = await p.firefox.launch(
                        headless=True,
                        args=["--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage"]
                    )
                    page = await browser.new_page()

                    logger.info("Loading homepage...")
                    await page.goto(homepage_url, wait_until="domcontentloaded", timeout=60000)
                    await asyncio.sleep(1.0)

                    content = await page.content()
                    soup = BeautifulSoup(content, "html.parser")

                    ltd_section = None
                    ltd_heading_text = None
                    for h2 in soup.find_all("h2"):
                        heading_text = _clean_text(h2.get_text(" ", strip=True))
                        if heading_text.startswith("Limited-Time Free"):
                            ltd_section = h2.find_parent("section")
                            ltd_heading_text = heading_text
                            break

                    if not ltd_section:
                        logger.warning(f"'Limited-Time Free' section not found. Attempt {attempt}/{retries}")
                        await browser.close()
                        await asyncio.sleep(random.uniform(5, 9))
                        continue

                    deadline_suffix = _parse_deadline_suffix(ltd_heading_text) if ltd_heading_text else None

                    # Collect items from the section without visiting each listing
                    items = []
                    seen = set()
                    for li in ltd_section.find_all("li"):
                        a = li.find("a", href=lambda h: h and h.startswith("/listings/"))
                        if not a:
                            continue
                        link = "https://www.fab.com" + a["href"]
                        if link in seen:
                            continue
                        seen.add(link)

                        # Try several ways to get a readable name from the card
                        prelim_name = _clean_text(a.get_text(" ", strip=True))
                        if not prelim_name:
                            aria = a.get("aria-label")
                            if aria:
                                prelim_name = _clean_text(aria)

                        img_tag = li.find("img")
                        thumb = img_tag["src"] if (img_tag and img_tag.get("src")) else None

                        # Normalize image URL
                        if thumb:
                            if thumb.startswith("//"):
                                thumb = "https:" + thumb
                            elif thumb.startswith("/"):
                                thumb = "https://www.fab.com" + thumb

                        items.append({"name": prelim_name or "Untitled Listing", "link": link, "image": thumb})

                    await browser.close()

                    if not items:
                        logger.info("Limited-Time Free section is empty on homepage.")
                        return [], deadline_suffix

                    logger.info(f"Collected {len(items)} listing cards from homepage.")
                    return items, deadline_suffix

            except Exception as e:
                logger.error(f"Homepage parse error: {e}. Retrying {attempt}/{retries}...")
                await asyncio.sleep(random.uniform(10, 15))

        logger.error("Failed to fetch Limited-Time Free assets after several attempts.")
        return None, None

    finally:
        if display:
            display.stop()


def is_admin(ctx: commands.Context):
    return ctx.guild is not None and ctx.author.guild_permissions.administrator


def is_dm(ctx: commands.Context):
    return ctx.guild is None


def load_data(filename):
    if os.path.exists(filename):
        with open(filename, 'r') as f:
            data = json.load(f)
            logger.info(f"Loaded {len(data) if isinstance(data, list) else '1'} objects from {filename}.")
            return data
    logger.warning(f"{filename} not found. Load failed.")
    return []


class EpicAssetsNotifyBot(commands.Bot):
    def __init__(self, command_prefix: str, token: str):
        intents = discord.Intents.default()
        intents.message_content = True
        super().__init__(command_prefix=command_prefix, intents=intents)
        self.token = token
        self.add_commands()
        self.data_folder = "/data/" if os.name != 'nt' else "data/"
        self.subscribed_channels = load_data(os.path.join(self.data_folder, 'subscribers_channels_backup.json'))
        self.subscribed_users = load_data(os.path.join(self.data_folder, 'subscribers_users_backup.json'))
        self.assets_list = load_data(os.path.join(self.data_folder, 'assets_backup.json'))
        self.deadline_suffix = ""
        try:
            if os.path.exists(os.path.join(self.data_folder, 'deadline_backup.json')):
                with open(os.path.join(self.data_folder, 'deadline_backup.json'), 'r') as f:
                    self.deadline_suffix = json.load(f) or ""
        except Exception:
            self.deadline_suffix = ""

        self.next_check_time = None
        self.delete_after = 10
        self.backup_delay = 900
        self.message_delay = 0.5

    async def on_ready(self):
        logger.info(f'Logged in as {self.user}')
        self.loop.create_task(self.set_daily_check())
        self.loop.create_task(self.backup_loop())

    def run_bot(self):
        if not os.path.exists(self.data_folder):
            logger.info(f"Creating data folder at {self.data_folder}")
            os.makedirs(self.data_folder)

        logger.info("Starting bot...")
        self.run(self.token)

    def _compose_header(self) -> str:
        month_name = get_month_name()
        if self.deadline_suffix:
            return f"## {month_name} ассеты от эпиков ({self.deadline_suffix})\n"
        return f"## {month_name} ассеты от эпиков\n"

    async def _build_message_and_files(self, assets):
        """Builds markdown message and downloads images using one ClientSession."""
        message = self._compose_header()
        files = []
        async with aiohttp.ClientSession() as session:
            for asset in assets:
                message += f"- [{asset['name']}](<{asset['link']}>)\n"
                img_url = asset.get('image')
                if not img_url:
                    continue
                try:
                    async with session.get(img_url, timeout=30) as resp:
                        image_data = await resp.read()
                    # Sanitize filename a bit
                    safe_name = re.sub(r'[\\/*?:"<>|]+', "_", asset['name'])[:100] or "image"
                    files.append(discord.File(BytesIO(image_data), filename=f"{safe_name}.png"))
                except Exception as e:
                    logger.warning(f"Image fetch failed for {asset['link']}: {e}")
        return message, files

    def add_commands(self):
        @self.command(name='sub')
        async def subscribe(ctx: commands.Context):
            if not is_admin(ctx) and not is_dm(ctx):
                await ctx.send("You do not have the necessary permissions to run this command.")
                return

            if is_dm(ctx):
                user_id = ctx.author.id
                if any(user['id'] == user_id for user in self.subscribed_users):
                    await ctx.send("You are already subscribed.")
                    return
                self.subscribed_users.append({'id': user_id, 'shown_assets': False})
                await ctx.send("Subscribed to asset updates")
                logger.info(f"User {ctx.author} subscribed to asset updates.")

                if self.assets_list and not self.subscribed_users[-1]['shown_assets']:
                    message, files = await self._build_message_and_files(self.assets_list)
                    user = await self.fetch_user(user_id)
                    await user.send(message, files=files)
                    self.subscribed_users[-1]['shown_assets'] = True

            else:
                channel_id = ctx.channel.id
                if any(channel['id'] == channel_id for channel in self.subscribed_channels):
                    await ctx.send("This channel is already subscribed.")
                    return
                self.subscribed_channels.append({'id': channel_id, 'shown_assets': False})
                await ctx.send(f"Subscribed to asset updates in: {ctx.channel.name}")
                logger.info(f"Channel {ctx.channel.name} subscribed to asset updates.")

                if self.assets_list and not self.subscribed_channels[-1]['shown_assets']:
                    message, files = await self._build_message_and_files(self.assets_list)
                    channel = self.get_channel(channel_id)
                    await channel.send(message, files=files)
                    self.subscribed_channels[-1]['shown_assets'] = True

        @self.command(name='unsub')
        async def unsubscribe(ctx: commands.Context):
            if not is_admin(ctx) and not is_dm(ctx):
                await ctx.send("You do not have the necessary permissions to run this command.")
                return

            if is_dm(ctx):
                user_id = ctx.author.id
                for user in self.subscribed_users:
                    if user['id'] == user_id:
                        self.subscribed_users.remove(user)
                        await ctx.send("Unsubscribed from asset updates.")
                        logger.info(f"User {ctx.author} unsubscribed from asset updates.")
                        return
                await ctx.send("You are not subscribed.")
            else:
                channel_id = ctx.channel.id
                for channel in self.subscribed_channels:
                    if channel['id'] == channel_id:
                        self.subscribed_channels.remove(channel)
                        await ctx.send("Unsubscribed from asset updates.")
                        logger.info(f"Channel {ctx.channel.name} unsubscribed from asset updates.")
                        return
                await ctx.send("This channel is not subscribed.")

        @self.command(name='time')
        async def time_left(ctx: commands.Context):
            if self.next_check_time:
                now = datetime.now()
                time_remaining = self.next_check_time - now
                hours, remainder = divmod(time_remaining.seconds, 3600)
                minutes, seconds = divmod(remainder, 60)
                message = (f"Time left until next check: {hours:02}:{minutes:02}:{seconds:02}\n"
                           f"-# This message will be deleted after {self.delete_after} seconds")
                sent_message = await ctx.send(message)
                await asyncio.sleep(self.delete_after)
                await sent_message.delete()
            else:
                message = (f"No scheduled check found.\n"
                           f"-# This message will be deleted after {self.delete_after} seconds")
                sent_message = await ctx.send(message)
                await asyncio.sleep(self.delete_after)
                await sent_message.delete()

        @subscribe.error
        @unsubscribe.error
        async def on_command_error(ctx: commands.Context, error: commands.CommandError):
            if isinstance(error, commands.MissingPermissions):
                await ctx.send("You do not have the necessary permissions to run this command.")

    async def set_daily_check(self):
        while True:
            self.next_check_time = datetime.now() + timedelta(days=1)
            await self.check_and_notify_assets()
            await asyncio.sleep(24 * 60 * 60)

    async def check_and_notify_assets(self):
        assets, deadline = await get_free_assets()
        if not assets:
            return

        def ids(lst):
            return {a['link'] for a in (lst or [])}

        new_ids = ids(assets)
        old_ids = ids(self.assets_list)
        deadline = deadline or ""
        deadline_changed = (deadline != (self.deadline_suffix or ""))

        added = new_ids - old_ids

        # Ignore shrink-only change (when the new set is a strict subset and no new links appeared)
        if not deadline_changed and not added and new_ids.issubset(old_ids) and new_ids != old_ids:
            logger.warning("Shrink-only change detected — likely transient scrape issue. Skipping update.")
            return

        # If nothing changed and no new deadline — do nothing
        if not deadline_changed and new_ids == old_ids:
            return

        # Update state & notify
        self.assets_list = assets
        self.deadline_suffix = deadline

        message, files = await self._build_message_and_files(assets)

        for channel in self.subscribed_channels:
            channel_obj = self.get_channel(channel['id'])
            if channel_obj:
                await channel_obj.send(message, files=files)
                channel['shown_assets'] = True
            await asyncio.sleep(self.message_delay)

        for user in self.subscribed_users:
            user_obj = await self.fetch_user(user['id'])
            if user_obj:
                try:
                    await user_obj.send(message, files=files)
                    user['shown_assets'] = True
                except Exception as e:
                    logger.warning(f"DM failed for {user['id']}: {e}")
            await asyncio.sleep(self.message_delay)

        await self.backup_data()

    async def backup_loop(self):
        while True:
            await self.backup_data()
            await asyncio.sleep(self.backup_delay)

    async def backup_data(self):
        with open(os.path.join(self.data_folder, 'subscribers_channels_backup.json'), 'w') as f:
            json.dump(self.subscribed_channels, f)
            logger.info(f"Saved {len(self.subscribed_channels)} subscribed channels to backup.")
        with open(os.path.join(self.data_folder, 'subscribers_users_backup.json'), 'w') as f:
            json.dump(self.subscribed_users, f)
            logger.info(f"Saved {len(self.subscribed_users)} subscribed users to backup.")
        with open(os.path.join(self.data_folder, 'assets_backup.json'), 'w') as f:
            json.dump(self.assets_list, f)
            logger.info(f"Saved {len(self.assets_list) if self.assets_list else 0} assets to backup.")
        with open(os.path.join(self.data_folder, 'deadline_backup.json'), 'w') as f:
            json.dump(self.deadline_suffix, f)
            logger.info("Saved deadline suffix to backup.")


if __name__ == '__main__':
    TOKEN = os.environ["ASSETS_BOT_TOKEN"]
    COMMAND_PREFIX = '/assets '

    bot = EpicAssetsNotifyBot(command_prefix=COMMAND_PREFIX, token=TOKEN)
    bot.run_bot()
