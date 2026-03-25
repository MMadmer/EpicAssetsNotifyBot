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
- SQLite database storage for subscriptions, user profiles, latest assets, and deadline state
- Safe one-time migration from legacy JSON backups to the database

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
| `ASSETS_BOT_DATA_DIR` | No | `/data` on Linux, `data` on Windows | Directory that stores the SQLite database and any legacy JSON files |
| `ASSETS_BOT_DATABASE_URL` | No | `sqlite+aiosqlite:///<data-dir>/bot.db` | Explicit SQLAlchemy connection string; if omitted, the bot uses SQLite inside the data directory |

## Database Migration

On the first startup with a fresh database, the bot automatically imports legacy JSON files from the data directory if they exist. The legacy files are left untouched for rollback safety.

You can also run the migration manually before deploying the new version:

```bash
python migrate_json_to_db.py
```

The script is idempotent: if the database already contains data, it exits without overwriting anything.

## Localization

All user-facing strings live in `locales/`. Add a new JSON catalog to introduce another language without touching the bot logic.

## License

MIT. See [LICENSE](LICENSE).
