FROM python:3.10-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    xvfb \
    wget \
    gnupg \
    curl \
    ca-certificates \
    && pip install --no-cache-dir \
    playwright \
    aiohttp \
    discord.py \
    beautifulsoup4 \
    loguru \
    pyvirtualdisplay \
    && playwright install --with-deps --force firefox && \
    rm -rf /usr/local/bin/chromium /usr/local/bin/webkit \
    && apt-get purge -y --auto-remove wget gnupg curl && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/* /root/.cache/pip /tmp/* /var/tmp/* /usr/share/doc /usr/share/man /usr/share/locale /usr/share/info /usr/share/lintian /usr/share/linda /var/cache/debconf/*-old /etc/apt/sources.list.d/*

ENV MOZ_REMOTE_SETTINGS_DEVTOOLS=1

WORKDIR /app
COPY main.py LICENSE README.md /app/

CMD ["python", "main.py"]
