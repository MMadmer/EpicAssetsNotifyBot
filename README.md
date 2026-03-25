# Epic Assets Notify Bot

<img width="1024" height="1024" alt="epic_assets_avatar" src="https://github.com/user-attachments/assets/3ba5ab1d-9fba-41a0-bae3-2a557eeefb91" />

Discord bot that tracks current `Limited-Time Free` assets on Fab and posts updates to subscribed Discord channels or direct messages.

## Features

- Daily Fab checks with change detection
- Channel and DM subscriptions
- Asset links with image attachments
- Externalized localization with `ru-RU` and `en-US`
- Simple JSON backups for subscriptions and the latest asset snapshot

## Commands

- `/assets sub`: subscribe the current channel or DM
- `/assets unsub`: unsubscribe the current channel or DM
- `/assets time`: show time left until the next check

## Quick Start

```bash
git clone https://github.com/MMadmer/EpicAssetsNotifyBot.git
cd EpicAssetsNotifyBot
pip install -r requirements.txt
playwright install firefox
```

Set `ASSETS_BOT_TOKEN` in your environment, then run:

```bash
python main.py
```

## Environment

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `ASSETS_BOT_TOKEN` | Yes | - | Discord bot token |
| `ASSETS_BOT_LOCALE` | No | `ru-RU` | Active locale catalog from `locales/` |

## Localization

All user-facing strings live in `locales/`. Add a new JSON catalog to introduce another language without touching the bot logic.

## License

MIT. See [LICENSE](LICENSE).