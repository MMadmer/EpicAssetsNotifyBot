FROM python:3.10-slim as build-stage

# Download system dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    xvfb \
    wget \
    gnupg \
    curl \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Download bot Python dependencies
RUN pip install --no-cache-dir \
    playwright \
    aiohttp \
    discord.py \
    beautifulsoup4 \
    loguru \
    pyvirtualdisplay \
    && playwright install --with-deps --force webkit && \
    rm -rf /usr/local/bin/chromium /usr/local/bin/firefox \
    && apt-get purge -y --auto-remove \
    && rm -rf /var/lib/apt/lists/* /root/.cache/pip

WORKDIR /app
COPY main.py LICENSE README.md /app/

# Run bot
CMD ["python", "main.py"]
