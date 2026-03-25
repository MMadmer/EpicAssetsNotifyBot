# Localization

Each locale is a UTF-8 JSON catalog named by its BCP 47 code, for example `ru-RU.json`.

How to add or update a locale:

1. Copy `ru-RU.json` to a new file like `en-US.json`.
2. Translate only the string values. Do not rename keys.
3. Keep placeholders like `{channel_name}` or `{hours:02}` unchanged.
4. Set `ASSETS_BOT_LOCALE` if you want to change the bot''s default locale.

Notes:

- `calendar.months.standalone` is used for headers like "Мартовские ассеты".
- `calendar.months.format` is used inside date phrases like "до 9 сентября".
- Missing keys fall back to `ru-RU.json`.
- Users can override the default locale for themselves with `/assets lang`.
- The repo currently ships with Arabic, Azerbaijani, Bengali, English, French, Georgian, German, Hindi, Japanese, Korean, Polish, Portuguese, Russian, Simplified Chinese, Spanish, Turkish, Ukrainian, and Urdu.