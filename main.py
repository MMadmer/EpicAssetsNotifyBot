import discord
from discord.ext import commands
import requests
from bs4 import BeautifulSoup
import asyncio
from datetime import datetime
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


class EpicGamesBot(commands.Bot):
    def __init__(self, command_prefix, token):
        intents = discord.Intents.default()
        intents.message_content = True  # Enable message content intents
        super().__init__(command_prefix=command_prefix, intents=intents)
        self.token = token
        self.current_channel = None  # Store the current channel for notifications
        self.assets_list = None  # Store the list of assets
        self.check_task = None  # Store the task for the timer
        self.add_commands()  # Register commands

    async def on_ready(self):
        # This function is called when the bot is ready
        print(f'Logged in as {self.user}')

    def run_bot(self):
        # This function runs the bot with the provided token
        self.run(self.token)

    def add_commands(self):
        # This function registers commands
        @self.command(name='assets_start')
        async def start(ctx):
            self.current_channel = ctx.channel  # Set the current channel for notifications
            if self.check_task:
                self.check_task.cancel()  # Cancel the previous timer if it was set
            self.check_task = self.loop.create_task(self.set_daily_check())  # Create a new daily check task
            await ctx.send(f"Started watching for asset updates in: {ctx.channel.name}")

        @self.command(name='assets_stop')
        async def stop(ctx):
            if self.check_task:
                self.check_task.cancel()  # Cancel the current timer
                self.check_task = None
                await ctx.send("Stopped watching for asset updates.")
            else:
                await ctx.send("No active watch task to stop.")

    async def set_daily_check(self):
        # This function sets a daily check for asset updates
        while True:
            await self.check_and_notify_assets()  # Check and notify about asset updates
            await asyncio.sleep(24 * 60 * 60)  # Wait for a day (24 hours)

    async def check_and_notify_assets(self):
        # This function checks for asset updates and notifies the current channel if there are new assets
        if not self.current_channel:
            print("Current channel is not set.")
            return

        new_assets = self.get_free_assets()  # Get the current list of free assets
        if new_assets and new_assets != self.assets_list:  # Check if the list of assets has changed
            self.assets_list = new_assets  # Update the stored list of assets
            month_name = get_month_name()
            message = f"## {month_name} ассеты от эпиков\n"
            files = []
            for asset in new_assets:
                message += f"- [{asset['name']}](<{asset['link']}>)\n"  # Format the message
                image_data = requests.get(asset['image']).content
                files.append(discord.File(BytesIO(image_data), filename=f"{asset['name']}.png"))
            await self.current_channel.send(message, files=files)  # Send the message and files to the current channel

    def get_free_assets(self):
        # This function retrieves the list of free assets from the Epic Games Store
        url = "https://www.unrealengine.com/marketplace/en-US/store"
        headers = {
            "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36",
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


if __name__ == '__main__':
    TOKEN = "YOUR_TOKEN_HERE"  # Replace with your bot token
    COMMAND_PREFIX = '/'

    bot = EpicGamesBot(command_prefix=COMMAND_PREFIX, token=TOKEN)
    bot.run_bot()
