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

## Общие принципы

### Конфигурация
Все сервисы используют `.env` файлы для конфигурации.

### Логирование
Используется `zap` logger с уровнями: debug, info, warn, error.

### Graceful Shutdown
Все сервисы поддерживают graceful shutdown через SIGINT/SIGTERM.

### Health Check
Каждый сервис предоставляет `/health` endpoint.
