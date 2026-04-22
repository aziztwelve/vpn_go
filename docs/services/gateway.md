# Gateway Service

API Gateway для VPN системы. Принимает HTTP запросы и маршрутизирует их к соответствующим gRPC микросервисам.

## Конфигурация

### Переменные окружения

```bash
# HTTP Server
HTTP_HOST=0.0.0.0
HTTP_PORT=8081

# Logging
LOG_LEVEL=info  # debug, info, warn, error
```

## API Endpoints

### Health Check
```
GET /health
```

**Response:**
```json
{
  "status": "ok",
  "service": "vpn-gateway"
}
```

### API v1
```
GET /api/v1
```

**Response:**
```json
{
  "status": "ok",
  "service": "vpn-gateway"
}
```

## Запуск

### Development
```bash
cd services/gateway
go run cmd/main/main.go
```

### Production
```bash
# Build
task build:gateway

# Run
./bin/gateway
```

### Через Taskfile
```bash
# Dev режим
task dev:gateway

# Build и запуск
task build-and-run
```

## Архитектура

```
gateway/
├── cmd/main/           # Entry point
├── internal/
│   ├── app/            # Application setup
│   ├── config/         # Configuration
│   ├── handler/        # HTTP handlers
│   ├── middleware/     # HTTP middleware
│   └── client/         # gRPC clients
├── .env                # Environment variables
└── go.mod
```

## Middleware

- **RequestID** - добавляет уникальный ID к каждому запросу
- **RealIP** - извлекает реальный IP клиента
- **Logger** - логирует все запросы
- **Recoverer** - восстанавливается после паники
- **Timeout** - таймаут 60 секунд на запрос
- **CORS** - разрешает cross-origin запросы

## Graceful Shutdown

Gateway поддерживает graceful shutdown:
- Обрабатывает SIGINT/SIGTERM
- Завершает текущие запросы
- Закрывает gRPC соединения
- Таймаут: 5 секунд

## Логирование

Используется `zap` logger:
- **Production mode** - JSON формат
- **Development mode** - человекочитаемый формат

Уровни логирования:
- `debug` - детальная информация для отладки
- `info` - общая информация о работе
- `warn` - предупреждения
- `error` - ошибки

## Мониторинг

### Health Check
```bash
curl http://localhost:8081/health
```

### Метрики
TODO: Добавить Prometheus метрики

## Troubleshooting

### Порт занят
```
Error: listen tcp 0.0.0.0:8081: bind: address already in use
```
Решение: измените `HTTP_PORT` в `.env` или остановите процесс на порту 8081.

### gRPC соединение не устанавливается
Проверьте что микросервисы запущены и доступны.
