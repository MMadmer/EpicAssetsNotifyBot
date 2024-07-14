import discord
from discord.ext import commands
import requests
from bs4 import BeautifulSoup


class EpicGamesBot(commands.Bot):
    def __init__(self, command_prefix, token):
        intents = discord.Intents.default()
        intents.message_content = True
        super().__init__(command_prefix=command_prefix, intents=intents)

        self.token = token
        self.current_channel = None
        self.add_commands()

    async def on_ready(self):
        print(f'Logged in as {self.user}')

    def run_bot(self):
        self.run(self.token)

    def add_commands(self):
        @self.command(name='set_channel')
        async def set_channel(ctx):
            self.current_channel = ctx.channel
            await ctx.send(f"Channel set to: {ctx.channel.name}")

        @self.command(name='free_assets')
        async def free_assets(ctx):
            assets = self.get_free_assets()
            if assets:
                for asset in assets:
                    await ctx.send(f"{asset['name']}\n{asset['link']}")
            else:
                await ctx.send("Не удалось получить список бесплатных ассетов.")

    def get_free_assets(self):
        url = "https://www.unrealengine.com/marketplace/en-US/store"
        headers = {
            "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36",
            "Accept-Language": "en-US,en;q=0.5",
            "Referer": "https://www.unrealengine.com/",
            "Origin": "https://www.unrealengine.com",
        }

        session = requests.Session()
        try:
            response = session.get(url, headers=headers)
            response.raise_for_status()
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
            name_element = element.find('h3')
            link_element = element.find('a', href=True)
            if name_element and link_element:
                asset_name = name_element.text.strip()
                asset_link = "https://www.unrealengine.com" + link_element['href']
                assets.append({'name': asset_name, 'link': asset_link})

        if not assets:
            print("No assets found in the 'Free For The Month' section.")
        return assets


if __name__ == '__main__':
    TOKEN = ""
    COMMAND_PREFIX = '/'

    bot = EpicGamesBot(command_prefix=COMMAND_PREFIX, token=TOKEN)
    bot.run_bot()
