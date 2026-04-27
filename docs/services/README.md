# Документация сервисов

Описание всех микросервисов VPN системы.

## Архитектура

VPN Backend построен на микросервисной архитектуре:

```
┌─────────────┐
│   Client    │
└──────┬──────┘
       │ HTTP
       ▼
┌─────────────┐
│   Gateway   │ ← API Gateway (HTTP → gRPC)
└──────┬──────┘
       │ gRPC
       ▼
┌─────────────────────────────┐
│     Microservices           │
│  ┌──────┐  ┌──────┐        │
│  │ Auth │  │ VPN  │  ...   │
│  └──────┘  └──────┘        │
└─────────────────────────────┘
       │
       ▼
┌─────────────┐
│  PostgreSQL │
└─────────────┘
```

## Сервисы

### Gateway
- **Порт:** 8081 (HTTP)
- **Описание:** API Gateway, маршрутизация HTTP → gRPC
- **Документация:** [gateway.md](./gateway.md)

### VPN Service + Xray
- **Порт:** 50062 (gRPC), Xray на 8443 (VLESS+Reality) + 10085 (Xray API)
- **Описание:** Генерация VLESS-ссылок, управление юзерами в Xray через gRPC API
- **Документация:**
  - [xray-integration.md](./xray-integration.md) — как связаны VPN Service, Xray и Postgres (диаграммы + маппинг полей + e2e-воспроизведение)
  - [device-limit.md](./device-limit.md) — лимит одновременных устройств через heartbeat Xray Stats (Этап 3)
  - [multi-server.md](./multi-server.md) — горизонтальное масштабирование: несколько Xray VPS, ResyncServer, load balancing (Этап 6)

### Auth / Security
- **Описание:** JWT-авторизация на Gateway, защита приватных ручек от чужих user_id
- **Документация:**
  - [auth-middleware.md](./auth-middleware.md) — как работает JWT middleware и проверка токенов (Этап 4)

### Payment Service
- **Порт:** 50063 (gRPC)
- **Описание:** Оплата через Telegram Stars — invoice links, webhook'и, идемпотентность, refund
- **Документация:**
  - [payment-integration.md](./payment-integration.md) — полный флоу оплаты через Telegram Stars (Этап 5)
  - [yoomoney-integration.md](./yoomoney-integration.md) — доп. канал: приём переводов на кошелёк ЮMoney (OAuth2, HTTP-уведомления)
  - [yoomoney-api-reference.md](./yoomoney-api-reference.md) — полный конспект всех 22 страниц официальной доки ЮMoney Wallet API
  - [wata-integration.md](./wata-integration.md) — доп. канал: WATA H2H API (карты/СБП/T-Pay/SberPay, RSA-подпись webhook)

## Общие принципы

### Конфигурация
Все сервисы используют `.env` файлы для конфигурации.

### Логирование
Используется `zap` logger с уровнями: debug, info, warn, error.

### Graceful Shutdown
Все сервисы поддерживают graceful shutdown через SIGINT/SIGTERM.

### Health Check
Каждый сервис предоставляет `/health` endpoint.
