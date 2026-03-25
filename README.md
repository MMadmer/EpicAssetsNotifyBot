# Epic Assets Notify Bot

<img width="1024" height="1024" alt="epic_assets_avatar" src="https://github.com/user-attachments/assets/3ba5ab1d-9fba-41a0-bae3-2a557eeefb91" />

Discord bot that tracks current `Limited-Time Free` assets on Fab and posts updates to subscribed Discord channels or direct messages.

## Features

- Daily Fab checks with change detection
- Channel and DM subscriptions
- Per-channel language on servers and personal language in DMs
- Built-in support for major world languages
- Asset links with image attachments
- Externalized localization catalogs in `locales/`
- Simple JSON backups for subscriptions, channel locales, DM user locales, and the latest asset snapshot

## Supported Languages

- `ar`: العربية
- `az`: Azərbaycanca
- `bn`: বাংলা
- `de`: Deutsch
- `en`: English
- `es`: Español
- `fr`: Français
- `hi`: हिन्दी
- `ja`: 日本語
- `ka`: ქართული
- `ko`: 한국어
- `pl`: Polski
- `pt`: Português
- `ru`: Русский
- `tr`: Türkçe
- `uk`: Українська
- `ur`: اردو
- `zh`: 简体中文

## Commands

- `/assets sub`: subscribe the current channel or DM
- `/assets unsub`: unsubscribe the current channel or DM
- `/assets time`: show time left until the next check
- `/assets lang <locale-code>`: in DMs changes your language, in server channels changes that channel language for admins only

`/assets lang` also shows the current language and available options. Aliases: `/assets locale`, `/assets l`.

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
| `ASSETS_BOT_LOCALE` | No | `ru-RU` | Default locale for DMs without a personal override and for channels without a saved locale |

## Localization

All user-facing strings live in `locales/`. Add a new JSON catalog to introduce another language without touching the bot logic.

## License

MIT. See [LICENSE](LICENSE).