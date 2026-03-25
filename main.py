import os

from epic_assets_notify_bot import create_bot


if __name__ == "__main__":
    TOKEN = os.environ["ASSETS_BOT_TOKEN"]
    COMMAND_PREFIX = "/assets "

    bot = create_bot(command_prefix=COMMAND_PREFIX, token=TOKEN)
    bot.run_bot()