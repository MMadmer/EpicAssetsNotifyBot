# Epic Games Monthly Free Assets Tracker Bot

![EpicAssetsNotifyBot](https://github.com/user-attachments/assets/c69a0594-4801-4b74-a2cc-bde7909922b1)

## Overview

The Epic Games Free Assets Tracker Bot is a Discord bot designed to help Unreal Engine developers stay updated with the latest free assets available on the Epic Games Store. This bot provides an automated solution for tracking, notifying, and displaying the newest free assets of the month, ensuring developers don't miss out on valuable resources.

## Features

- **Automated Daily Checks**: The bot performs daily checks for new free assets and notifies subscribed Discord channels and users if there are updates.
- **Admin and DM Commands**: Users with administrator permissions can manage subscriptions, and individual users can subscribe/unsubscribe via direct messages.
- **Time Left Notification**: Users can query the bot to find out how much time is left until the next asset check. The bot provides a formatted response and automatically deletes the message after a short period.
- **Image Attachments**: When new assets are detected, the bot sends a detailed message with asset names, links, and attached images.

## Commands

### Admin Commands

- `/assets sub`: Subscribes the current server channel to asset updates. Can only be run by administrators.
- `/assets unsub`: Unsubscribes the server from asset updates. Can only be run by administrators.

### General Commands

- `/assets sub`: Subscribes the user to asset updates via direct message.
- `/assets unsub`: Unsubscribes the user from asset updates via direct message.
- `/assets time`: Displays the time remaining until the next check for new assets. This message is automatically deleted after 10 seconds.

## How It Works

1. **Daily Checks**: The bot uses a background task to check for new assets every 24 hours. The time of the next check is stored and updated after each check.
2. **Asset Retrieval**: The bot scrapes the Epic Games Store page for the latest free assets using BeautifulSoup and Requests libraries.
3. **Notifications**: If new assets are found, the bot sends a message to the designated channels and subscribed users with the asset details and images.

## Installation

1. Clone the repository:
   ```bash
   git clone https://github.com/MMadmer/EpicAssetsNotifyBot.git
   ```
2. Install the required dependencies:
   ```bash
   pip install -r requirements.txt
   ```
3. Set up your Discord bot token:
- Go to the Discord Developer Portal.
- Create a new application and bot.
- Copy the bot token and replace YOUR_TOKEN_HERE in the code.
4. Run the bot:
  ```bash
  python main.py
  ```

## Usage

To use the bot, invite it to your Discord server and use the commands listed above. Ensure that the bot has the necessary permissions to read and send messages in the desired channels.

## Contributing

Contributions are welcome! Feel free to submit a pull request or open an issue to suggest improvements or report bugs.

## License

This project is licensed under the MIT License. See the [LICENSE](LICENSE) file for details.

---

With this bot, Unreal Engine developers can easily stay up-to-date with the latest free assets from the Epic Games Store, enhancing their development workflow and ensuring they never miss out on valuable resources.

