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
from pyvirtualdisplay import Display
from loguru import logger

logger.add("bot.log", rotation="10 MB", level="INFO")


def get_month_name():
    month_names = {
        1: "Январские", 2: "Февральские", 3: "Мартовские", 4: "Апрельские",
        5: "Майские", 6: "Июньские", 7: "Июльские", 8: "Августовские",
        9: "Сентябрьские", 10: "Октябрьские", 11: "Ноябрьские", 12: "Декабрьские"
    }
    current_month = datetime.now().month

    return month_names[current_month]


async def get_free_assets(retries=5):
    url = "https://www.unrealengine.com/marketplace/en-US/store"
    display = Display(visible=False, size=(1920, 1080)) if not os.getenv("DISPLAY") else None

    if display:
        display.start()

    for attempt in range(retries):
        try:
            async with async_playwright() as p:
                browser = await p.webkit.launch(headless=False)  # Headless bypass
                page = await browser.new_page()

                await page.goto(url)

                await page.wait_for_selector('section.assets-block.marketplace-home-free')

                # Get HTML
                content = await page.content()

                await browser.close()
                if display:
                    display.stop()

                # Get assets
                soup = BeautifulSoup(content, 'html.parser')
                free_assets_section = soup.find('section', class_='assets-block marketplace-home-free')
                if not free_assets_section:
                    logger.warning("Could not find the 'Free For The Month' section.")
                    return None

                asset_elements = free_assets_section.find_all('div', class_='asset-container')

                assets = []
                for element in asset_elements:
                    name_element = element.find('h3')
                    link_element = element.find('a', href=True)
                    image_element = element.find('img')
                    if name_element and link_element and image_element:
                        asset_name = name_element.text.strip()
                        asset_link = "https://www.unrealengine.com" + link_element['href']
                        asset_image = image_element['src']
                        assets.append({'name': asset_name, 'link': asset_link, 'image': asset_image})

                if not assets:
                    logger.info("No assets found in the 'Free For The Month' section.")
                else:
                    logger.info(f"Found {len(assets)} free assets.")

                return assets

        except Exception as e:
            logger.error(f"An error occurred: {str(e)}. Retrying...")
            await asyncio.sleep(random.uniform(10, 15))

    logger.error("Failed to fetch the page after several attempts.")
    if display:
        display.stop()

    return None


def is_admin(ctx: commands.Context):
    return ctx.guild is not None and ctx.author.guild_permissions.administrator


def is_dm(ctx: commands.Context):
    return ctx.guild is None


def load_data(filename):
    if os.path.exists(filename):
        with open(filename, 'r') as f:
            data = json.load(f)
            logger.info(f"Loaded {len(data)} objects from {filename}.")
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
        self.subscribed_channels = load_data(
            os.path.join(self.data_folder, 'subscribers_channels_backup.json'))
        self.subscribed_users = load_data(
            os.path.join(self.data_folder, 'subscribers_users_backup.json'))
        self.assets_list = load_data(
            os.path.join(self.data_folder, 'assets_backup.json'))
        self.next_check_time = None
        self.delete_after = 10
        self.backup_delay = 900

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
                    month_name = get_month_name()
                    message = f"## {month_name} ассеты от эпиков\n"
                    files = []
                    for asset in self.assets_list:
                        message += f"- [{asset['name']}](<{asset['link']}>)\n"
                        async with aiohttp.ClientSession() as session:
                            async with session.get(asset['image']) as resp:
                                image_data = await resp.read()
                        files.append(discord.File(BytesIO(image_data), filename=f"{asset['name']}.png"))
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
                    month_name = get_month_name()
                    message = f"## {month_name} ассеты от эпиков\n"
                    files = []
                    for asset in self.assets_list:
                        message += f"- [{asset['name']}](<{asset['link']}>)\n"
                        async with aiohttp.ClientSession() as session:
                            async with session.get(asset['image']) as resp:
                                image_data = await resp.read()
                        files.append(discord.File(BytesIO(image_data), filename=f"{asset['name']}.png"))
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
        new_assets = await get_free_assets()
        if new_assets and new_assets != self.assets_list:
            self.assets_list = new_assets
            month_name = get_month_name()
            message = f"## {month_name} ассеты от эпиков\n"
            files = []
            for asset in new_assets:
                message += f"- [{asset['name']}](<{asset['link']}>)\n"
                async with aiohttp.ClientSession() as session:
                    async with session.get(asset['image']) as resp:
                        image_data = await resp.read()
                files.append(discord.File(BytesIO(image_data), filename=f"{asset['name']}.png"))

            for channel in self.subscribed_channels:
                channel_id = channel['id']
                channel_obj = self.get_channel(channel_id)
                if channel_obj:
                    await channel_obj.send(message, files=files)
                    channel['shown_assets'] = True

            for user in self.subscribed_users:
                user_id = user['id']
                user_obj = await self.fetch_user(user_id)
                if user_obj:
                    await user_obj.send(message, files=files)
                    user['shown_assets'] = True

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


if __name__ == '__main__':
    TOKEN = os.environ["ASSETS_BOT_TOKEN"]
    COMMAND_PREFIX = '/assets '

    bot = EpicAssetsNotifyBot(command_prefix=COMMAND_PREFIX, token=TOKEN)
    bot.run_bot()
