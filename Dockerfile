# Используем базовый образ с Python 3.10
FROM python:3.10-slim

# Установим необходимые системные зависимости
RUN apt-get update && apt-get install -y --no-install-recommends \
    xvfb \
    xauth \
    wget \
    gnupg \
    curl \
    ca-certificates \
    sudo \
    && rm -rf /var/lib/apt/lists/*

# Обновим pip и установим Python-зависимости
RUN pip install --no-cache-dir --upgrade pip \
    && pip install --no-cache-dir \
    playwright \
    aiohttp \
    discord.py \
    beautifulsoup4 \
    loguru \
    pyvirtualdisplay

# Установка зависимостей для Playwright и установка webkit-браузера
RUN playwright install-deps \
    && python -m playwright install webkit

# Установка рабочей директории и копирование содержимого проекта в контейнер
WORKDIR /app
COPY . /app

# Запуск Python-скрипта
CMD ["python", "main.py"]
