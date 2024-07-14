import discord
from discord.ext import commands
import requests
from bs4 import BeautifulSoup
import asyncio
from datetime import datetime, timedelta
from io import BytesIO


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
            asset_link = "https://www.unrealengine.com" + link_element[
                'href']  # Get the href attribute of the link element
            asset_image = image_element['src']  # Get the src attribute of the image element
            assets.append(
                {'name': asset_name, 'link': asset_link, 'image': asset_image})  # Add the asset to the list

    if not assets:
        print("No assets found in the 'Free For The Month' section.")
    return assets


class EpicAssetsNotifyBot(commands.Bot):
    def __init__(self, command_prefix, token):
        intents = discord.Intents.default()
        intents.message_content = True  # Enable message content intents
        super().__init__(command_prefix=command_prefix, intents=intents)
        self.token = token
        self.servers_data = {}  # Store data for each server
        self.add_commands()  # Register commands

    async def on_ready(self):
        # This function is called when the bot is ready
        print(f'Logged in as {self.user}')

    def run_bot(self):
        # This function runs the bot with the provided token
        self.run(self.token)

    def add_commands(self):
        # This function registers commands
        @self.command(name='start')
        @commands.has_permissions(administrator=True)
        async def start(ctx):
            guild_id = ctx.guild.id
            if guild_id in self.servers_data:
                await ctx.send("Asset tracking is already running. Please stop it first before starting again.")
                return

            self.servers_data[guild_id] = {
                'current_channel': ctx.channel,
                'assets_list': None,
                'check_task': self.loop.create_task(self.set_daily_check(ctx.guild.id))
            }
            await ctx.send(f"Started watching for asset updates in: {ctx.channel.name}")

        @self.command(name='stop')
        @commands.has_permissions(administrator=True)
        async def stop(ctx):
            guild_id = ctx.guild.id
            if guild_id in self.servers_data:
                self.servers_data[guild_id]['check_task'].cancel()  # Cancel the current timer
                del self.servers_data[guild_id]  # Remove the server data
                await ctx.send("Stopped watching for asset updates and cleared the asset list.")
            else:
                await ctx.send("No active watch task to stop.")

        @self.command(name='time')
        async def time_left(ctx):
            delete_after = 10
            guild_id = ctx.guild.id
            if guild_id in self.servers_data and 'next_check_time' in self.servers_data[guild_id]:
                now = datetime.now()
                time_remaining = self.servers_data[guild_id]['next_check_time'] - now
                hours, remainder = divmod(time_remaining.seconds, 3600)
                minutes, seconds = divmod(remainder, 60)
                message = (f"Time left until next check: {hours:02}:{minutes:02}:{seconds:02}\n"
                           f"-# Сообщение будет удалено через {delete_after} секунд")
                await ctx.send(message, delete_after=delete_after)
            else:
                await ctx.send("No active watch task.", delete_after=delete_after)

        @start.error
        @stop.error
        async def on_command_error(ctx, error):
            if isinstance(error, commands.MissingPermissions):
                await ctx.send("You do not have the necessary permissions to run this command.")

    async def set_daily_check(self, guild_id):
        while True:
            self.servers_data[guild_id]['next_check_time'] = datetime.now() + timedelta(days=1)
            await self.check_and_notify_assets(guild_id)
            await asyncio.sleep(24 * 60 * 60)

    async def check_and_notify_assets(self, guild_id):
        new_assets = get_free_assets()  # Get the current list of free assets
        if new_assets and new_assets != self.servers_data[guild_id][
            'assets_list']:  # Check if the list of assets has changed
            self.servers_data[guild_id]['assets_list'] = new_assets  # Update the stored list of assets
            month_name = get_month_name()
            message = f"## {month_name} ассеты от эпиков\n"
            files = []
            for asset in new_assets:
                message += f"- [{asset['name']}](<{asset['link']}>)\n"  # Format the message
                image_data = requests.get(asset['image']).content
                files.append(discord.File(BytesIO(image_data), filename=f"{asset['name']}.png"))
            await self.servers_data[guild_id]['current_channel'].send(message,
                                                                      files=files)  # Send the message and files to the current channel


if __name__ == '__main__':
    TOKEN = "YOUR_TOKEN_HERE"  # Replace with your bot token
    COMMAND_PREFIX = '/assets '

    bot = EpicAssetsNotifyBot(command_prefix=COMMAND_PREFIX, token=TOKEN)
    bot.run_bot()
