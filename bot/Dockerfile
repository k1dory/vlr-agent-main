# vlr-main-bot — Telegram subscription-gate bot.
# Один образ, команда (bot/worker) задаётся в compose.
FROM python:3.12-slim

ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
WORKDIR /srv

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY app ./app

# Том для SQLite-леджера.
VOLUME ["/data"]

# По умолчанию — бот; worker переопределяет command в compose.
CMD ["python", "-m", "app.bot"]
