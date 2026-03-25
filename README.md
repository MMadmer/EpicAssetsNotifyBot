# Epic Assets Notify Bot

<img width="1024" height="1024" alt="epic_assets_avatar" src="https://github.com/user-attachments/assets/3ba5ab1d-9fba-41a0-bae3-2a557eeefb91" />

Discord bot that tracks current `Limited-Time Free` assets on Fab and posts updates to subscribed Discord channels or direct messages.

## Features

- Daily Fab checks with change detection
- Server-level notification config with explicit channel/thread target, role mention, enable/disable switch, and test command
- Channel and DM subscriptions with backward-compatible aliases
- Per-server language on servers and personal language in DMs
- Built-in support for major world languages
- Asset links with optional image attachments per server
- Externalized localization catalogs in `locales/`
- SQLite storage for subscriptions, user profiles, latest assets, and deadline state
- One-time legacy JSON migration through the Go migrator

## Supported Languages

- `ar`
- `az`
- `bn`
- `de`
- `en`
- `es`
- `fr`
- `hi`
- `ja`
- `ka`
- `ko`
- `pl`
- `pt`
- `ru`
- `tr`
- `uk`
- `ur`
- `zh`

## Commands

- `/assets sub`: in DMs subscribes you, in servers enables updates and binds the current channel or thread
- `/assets unsub`: in DMs unsubscribes you, in servers disables updates without deleting the server config
- `/assets enable` / `/assets disable`: explicit server enable/disable controls
- `/assets set-channel`: set the current channel as the notification channel
- `/assets set-thread`: set the current thread as the notification target
- `/assets clear-thread`: remove the thread target and fall back to the configured channel
- `/assets set-role @role`: configure a role mention for notifications
- `/assets clear-role`: remove the configured role mention
- `/assets images <on|off>`: toggle image attachments for this server
- `/assets settings`: show the current server or DM configuration
- `/assets test`: send a test notification to the configured target
- `/assets time`: show time left until the next check
- `/assets lang <locale-code>`: in DMs changes your language, in servers changes the server notification language for admins only

Aliases preserved: `/assets locale`, `/assets l`, `/assets on`, `/assets off`, `/assets setchannel`, `/assets setthread`, `/assets clearthread`, `/assets setrole`, `/assets clearrole`, `/assets config`.

## Quick Start

Requirements:

- Go 1.26.1+
- A Discord bot token in `ASSETS_BOT_TOKEN`
- Playwright for Go installed with Firefox support for local runs:
  `go run github.com/playwright-community/playwright-go/cmd/playwright@v0.5700.1 install firefox`
- Optional: `ASSETS_BOT_BROWSER` pointing to a Mozilla Firefox executable if you want to use a system Firefox instead of the bundled Playwright Firefox

Run the bot:

```bash
go run ./cmd/bot
```

Run the one-time legacy JSON migration:

```bash
go run ./cmd/migrate_legacy
```

## Environment

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `ASSETS_BOT_TOKEN` | Yes | - | Discord bot token |
| `ASSETS_BOT_LOCALE` | No | `ru-RU` | Default locale for DMs without a personal override and for channels without a saved locale |
| `ASSETS_BOT_DATA_DIR` | No | `/data` on Linux, `data` on Windows | Directory that stores the SQLite database |
| `ASSETS_BOT_DATABASE_URL` | No | `sqlite+aiosqlite:///<data-dir>/bot.db` | Explicit SQLite connection string; if omitted, the bot uses SQLite inside the data directory |
| `ASSETS_BOT_BROWSER` | No | Playwright Firefox | Optional Firefox executable or absolute path used for headless Fab scraping |

## Project Layout

- `cmd/bot`: main bot entrypoint
- `cmd/migrate_legacy`: one-time legacy JSON importer
- `internal/app`: bootstrap and store adapter
- `internal/discord`: Discord runtime, commands, scheduling, delivery
- `internal/fab`: Fab scrape/parser logic
- `internal/store/sqlite`: SQLite persistence layer
- `internal/i18n`: locale loading and translation
- `internal/model`: domain model and normalization
- `locales/`: localization catalogs

## Docker

Default bot image:

```bash
docker build -t epic-assets-notify-bot .
docker run --rm -e ASSETS_BOT_TOKEN=... epic-assets-notify-bot
```

Optional legacy migration image:

```bash
docker build --target migrate-legacy -t epic-assets-migrate .
docker run --rm -e ASSETS_BOT_DATA_DIR=/data -v $(pwd)/data:/data epic-assets-migrate
```

## License

MIT. See `LICENSE`.
