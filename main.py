import discord
from discord.ext import commands
import requests
from bs4 import BeautifulSoup
import asyncio
from datetime import datetime, timedelta
from io import BytesIO
import json
import os


def get_month_name():
    # This function returns the modified month name
    month_names = {
        1: "Январские",
        2: "Февральские",
        3: "Мартовские",
        4: "Апрельские",
        5: "Майские",
        6: "Июньские",
        7: "Июльские",
        8: "Августовские",
        9: "Сентябрьские",
        10: "Октябрьские",
        11: "Ноябрьские",
        12: "Декабрьские"
    }
    current_month = datetime.now().month
    return month_names[current_month]


def get_free_assets():
    # This function retrieves the list of free assets from the Epic Games Store
    url = "https://www.unrealengine.com/marketplace/en-US/store"
    headers = {
        "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) "
                      "Chrome/91.0.4472.124 Safari/537.36",
        "Accept-Language": "en-US,en;q=0.5",
        "Referer": "https://www.unrealengine.com/",
        "Origin": "https://www.unrealengine.com",
        "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
        "Connection": "keep-alive",
        "Upgrade-Insecure-Requests": "1",
        "Sec-Fetch-Dest": "document",
        "Sec-Fetch-Mode": "navigate",
        "Sec-Fetch-Site": "none",
        "Sec-Fetch-User": "?1",
        "Pragma": "no-cache",
        "Cache-Control": "no-cache"
    }

    session = requests.Session()
    try:
        response = session.get(url, headers=headers)  # Make a request to the store page
        response.raise_for_status()  # Raise an exception for HTTP errors
    except requests.RequestException as e:
        print(f"Error fetching the page: {e}")
        return None

    if response.status_code != 200:
        print(response.status_code)
        return None

    soup = BeautifulSoup(response.content, 'html.parser')
    free_assets_section = soup.find('section', class_='assets-block marketplace-home-free')
    if not free_assets_section:
        print("Could not find the 'Free For The Month' section.")
        return None

    asset_elements = free_assets_section.find_all('div', class_='asset-container')

    assets = []
    for element in asset_elements:
        name_element = element.find('h3')  # Find the name of the asset
        link_element = element.find('a', href=True)  # Find the link to the asset
        image_element = element.find('img')  # Find the image of the asset
        if name_element and link_element and image_element:
            asset_name = name_element.text.strip()  # Get the text of the name element
            # Get the href attribute of the link element
            asset_link = "https://www.unrealengine.com" + link_element['href']
            asset_image = image_element['src']  # Get the src attribute of the image element
            assets.append({'name': asset_name, 'link': asset_link, 'image': asset_image})  # Add the asset to the list

    if not assets:
        print("No assets found in the 'Free For The Month' section.")
    return assets


def is_admin(ctx: commands.Context):
    return ctx.guild is not None and ctx.author.guild_permissions.administrator


def is_dm(ctx: commands.Context):
    return ctx.guild is None


def load_data(filename):
    if os.path.exists(filename):
        with open(filename, 'r') as f:
            data = json.load(f)
            print(f"Loaded {len(data)} objects from {filename}.")
            return data

    print(f"{filename} not found. Load failed.")

    return []


class EpicAssetsNotifyBot(commands.Bot):
    def __init__(self, command_prefix: str, token: str):
        intents = discord.Intents.default()
        intents.message_content = True  # Enable message content intents
        super().__init__(command_prefix=command_prefix, intents=intents)
        self.token = token
        self.add_commands()  # Register commands

        self.data_folder = self.data_folder = "/data/" if os.name != 'nt' else "data/"  # Folder for storing backup data
        self.subscribed_channels = load_data(
            os.path.join(self.data_folder, 'subscribers_channels_backup.json'))  # Load subscribed channels from backup
        self.subscribed_users = load_data(
            os.path.join(self.data_folder, 'subscribers_users_backup.json'))  # Load subscribed users from backup
        self.assets_list = load_data(
            os.path.join(self.data_folder, 'assets_backup.json'))  # Load assets list from backup
        self.next_check_time = None  # Store the next check time
        self.delete_after = 10  # Time after which the message will be deleted
        self.backup_delay = 900  # Backup delay in seconds

    async def on_ready(self):
        # This function is called when the bot is ready
        print(f'Logged in as {self.user}')
        self.loop.create_task(self.set_daily_check())  # Start the daily check task
        self.loop.create_task(self.backup_data())  # Start the backup task

    def run_bot(self):
        # This function runs the bot with the provided token
        self.run(self.token)

    def add_commands(self):
        # This function registers commands
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

                # Check and send current assets if not shown
                if self.assets_list and not self.subscribed_users[-1]['shown_assets']:
                    month_name = get_month_name()
                    message = f"## {month_name} ассеты от эпиков\n"
                    files = []
                    for asset in self.assets_list:
                        message += f"- [{asset['name']}](<{asset['link']}>)\n"
                        image_data = requests.get(asset['image']).content
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

                # Check and send current assets if not shown
                if self.assets_list and not self.subscribed_channels[-1]['shown_assets']:
                    month_name = get_month_name()
                    message = f"## {month_name} ассеты от эпиков\n"
                    files = []
                    for asset in self.assets_list:
                        message += f"- [{asset['name']}](<{asset['link']}>)\n"
                        image_data = requests.get(asset['image']).content
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
                        return
                await ctx.send("You are not subscribed.")
            else:
                channel_id = ctx.channel.id
                for channel in self.subscribed_channels:
                    if channel['id'] == channel_id:
                        self.subscribed_channels.remove(channel)
                        await ctx.send("Unsubscribed from asset updates.")
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
        new_assets = get_free_assets()  # Get the current list of free assets
        if new_assets and new_assets != self.assets_list:  # Check if the list of assets has changed
            self.assets_list = new_assets  # Update the stored list of assets
            month_name = get_month_name()
            message = f"## {month_name} ассеты от эпиков\n"
            files = []
            for asset in new_assets:
                message += f"- [{asset['name']}](<{asset['link']}>)\n"  # Format the message
                image_data = requests.get(asset['image']).content
                files.append(discord.File(BytesIO(image_data), filename=f"{asset['name']}.png"))

            for channel in self.subscribed_channels:
                channel_id = channel['id']
                channel_obj = self.get_channel(channel_id)
                if channel_obj:
                    await channel_obj.send(message,
                                           files=files)  # Send the message and files to the subscribed channels
                    channel['shown_assets'] = True

            for user in self.subscribed_users:
                user_id = user['id']
                user_obj = await self.fetch_user(user_id)
                if user_obj:
                    await user_obj.send(message, files=files)  # Send the message and files to subscribed users
                    user['shown_assets'] = True

    async def backup_data(self):
        while True:
            with open(os.path.join(self.data_folder, 'subscribers_channels_backup.json'), 'w') as f:
                json.dump(self.subscribed_channels, f)
                print(f"Saved {len(self.subscribed_channels)} subscribed channels to backup.")
            with open(os.path.join(self.data_folder, 'subscribers_users_backup.json'), 'w') as f:
                json.dump(self.subscribed_users, f)
                print(f"Saved {len(self.subscribed_users)} subscribed users to backup.")
            with open(os.path.join(self.data_folder, 'assets_backup.json'), 'w') as f:
                json.dump(self.assets_list, f)
                print(f"Saved {len(self.assets_list) if self.assets_list else 0} assets to backup.")
            await asyncio.sleep(self.backup_delay)


if __name__ == '__main__':
    TOKEN = os.environ["ASSETS_BOT_TOKEN"]  # Replace with your bot token
    COMMAND_PREFIX = '/assets '

    bot = EpicAssetsNotifyBot(command_prefix=COMMAND_PREFIX, token=TOKEN)
    bot.run_bot()
