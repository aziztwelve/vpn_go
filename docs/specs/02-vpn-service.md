# 02. VPN Service - Спецификация

## Описание

Сервис управления Xray VLESS серверами и пользователями. Отвечает за создание UUID, управление пользователями через Xray API, генерацию VLESS ссылок и контроль лимита одновременных подключений.

## Требования

### Функциональные:
- Управление Xray серверами (CRUD)
- Генерация UUID для пользователей
- Добавление/удаление пользователей в Xray через API
- Генерация VLESS ссылок для всех серверов
- Контроль лимита одновременных подключений
- Сбор активных подключений через Xray Stats API
- Получение списка серверов по локациям

### Нефункциональные:
- Время генерации UUID < 10ms
- Поддержка 500+ RPS
- Один UUID на все серверы
- Graceful shutdown

## Схема базы данных

Использует таблицы `vpn_servers`, `vpn_users` и `active_connections`:

```sql
CREATE TABLE vpn_servers (
    id SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    country_code VARCHAR(2) NOT NULL,
    city VARCHAR(100),
    host VARCHAR(255) NOT NULL,
    port INT NOT NULL DEFAULT 443,
    public_key VARCHAR(100) NOT NULL,
    private_key VARCHAR(100) NOT NULL,
    short_id VARCHAR(50) NOT NULL,
    dest VARCHAR(255) NOT NULL DEFAULT 'github.com:443',
    server_names JSONB NOT NULL DEFAULT '["github.com"]',
    api_host VARCHAR(255) NOT NULL DEFAULT '127.0.0.1',
    api_port INT NOT NULL DEFAULT 10085,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    max_users INT NOT NULL DEFAULT 1000,
    current_users INT NOT NULL DEFAULT 0
);

CREATE TABLE vpn_users (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    subscription_id BIGINT NOT NULL REFERENCES subscriptions(id),
    uuid UUID NOT NULL UNIQUE,
    email VARCHAR(255) NOT NULL UNIQUE,
    flow VARCHAR(50) NOT NULL DEFAULT 'xtls-rprx-vision',
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE active_connections (
    id BIGSERIAL PRIMARY KEY,
    vpn_user_id BIGINT NOT NULL REFERENCES vpn_users(id),
    server_id INT NOT NULL REFERENCES vpn_servers(id),
    device_identifier VARCHAR(255),
    connected_at TIMESTAMPTZ DEFAULT NOW(),
    last_seen TIMESTAMPTZ DEFAULT NOW()
);
```

## API (gRPC)

### Proto Definition

```protobuf
syntax = "proto3";

package vpn.v1;

option go_package = "github.com/vpn/shared/pkg/proto/vpn.v1";

service VPNService {
  // Получить список активных серверов
  rpc ListServers(ListServersRequest) returns (ListServersResponse);
  
  // Создать Xray пользователя (один UUID на все серверы)
  rpc CreateVPNUser(CreateVPNUserRequest) returns (CreateVPNUserResponse);
  
  // Получить VLESS ссылки для всех серверов
  rpc GetVLESSLinks(GetVLESSLinksRequest) returns (GetVLESSLinksResponse);
  
  // Удалить Xray пользователя
  rpc DeleteVPNUser(DeleteVPNUserRequest) returns (DeleteVPNUserResponse);
  
  // Получить активные подключения
  rpc GetActiveConnections(GetActiveConnectionsRequest) returns (GetActiveConnectionsResponse);
  
  // Проверить лимит устройств
  rpc CheckDeviceLimit(CheckDeviceLimitRequest) returns (CheckDeviceLimitResponse);
  
  // Обновить активные подключения (cron job)
  rpc UpdateActiveConnections(UpdateActiveConnectionsRequest) returns (UpdateActiveConnectionsResponse);
  
  // Admin: Создать сервер
  rpc CreateServer(CreateServerRequest) returns (CreateServerResponse);
  
  // Admin: Обновить сервер
  rpc UpdateServer(UpdateServerRequest) returns (UpdateServerResponse);
  
  // Admin: Удалить сервер
  rpc DeleteServer(DeleteServerRequest) returns (DeleteServerResponse);
}

message ListServersRequest {
  string country_code = 1; // Фильтр по стране
}

message ListServersResponse {
  repeated VPNServer servers = 1;
}

message CreateVPNUserRequest {
  int64 user_id = 1;
  int64 subscription_id = 2;
}

message CreateVPNUserResponse {
  VPNUser vpn_user = 1;
  repeated VLESSLink vless_links = 2; // Ссылки для всех серверов
}

message GetVLESSLinksRequest {
  int64 vpn_user_id = 1;
}

message GetVLESSLinksResponse {
  repeated VLESSLink vless_links = 1;
}

message DeleteVPNUserRequest {
  int64 vpn_user_id = 1;
}

message DeleteVPNUserResponse {
  bool success = 1;
}

message GetActiveConnectionsRequest {
  int64 vpn_user_id = 1;
}

message GetActiveConnectionsResponse {
  repeated ActiveConnection connections = 1;
  int32 total_count = 2;
  int32 max_devices = 3;
}

message CheckDeviceLimitRequest {
  int64 vpn_user_id = 1;
}

message CheckDeviceLimitResponse {
  bool can_connect = 1;
  int32 current_devices = 2;
  int32 max_devices = 3;
}

message UpdateActiveConnectionsRequest {
  // Вызывается cron job каждые 5 минут
}

message UpdateActiveConnectionsResponse {
  int32 updated_count = 1;
}

message VPNServer {
  int32 id = 1;
  string name = 2;
  string country_code = 3;
  string city = 4;
  string host = 5;
  int32 port = 6;
  string public_key = 7;
  string short_id = 8;
  string dest = 9;
  repeated string server_names = 10;
  bool is_active = 11;
  int32 max_users = 12;
  int32 current_users = 13;
}

message VPNUser {
  int64 id = 1;
  int64 user_id = 2;
  int64 subscription_id = 3;
  string uuid = 4;
  string email = 5;
  string flow = 6;
  string created_at = 7;
}

message VLESSLink {
  int32 server_id = 1;
  string server_name = 2;
  string country_code = 3;
  string link = 4; // vless://UUID@HOST:PORT?...
}

message ActiveConnection {
  int32 server_id = 1;
  string server_name = 2;
  string device_identifier = 3;
  string connected_at = 4;
  string last_seen = 5;
}
```

## План реализации

### Этап 1: Структура проекта (1 час)
- Создать директории `services/xray-service/{cmd,internal,migrations}`
- Создать `go.mod`
- Создать `.env` файл
- Создать `main.go`

### Этап 2: Proto и генерация (30 минут)
- Создать `shared/proto/vpn.v1/xray.proto`
- Сгенерировать Go код
- Проверить импорты

### Этап 3: Конфигурация (30 минут)
- `internal/config/config.go`
- Переменные окружения (gRPC порт, DB)

### Этап 4: Xray API Client (3 часа)
- `internal/xray/client.go`
- gRPC клиент для Xray API
- Методы:
  - `AddUser(serverHost, apiPort, tag, email, uuid, flow)` 
  - `RemoveUser(serverHost, apiPort, tag, email)`
  - `GetStats(serverHost, apiPort, email)` - получить трафик
  - `QueryStats(serverHost, apiPort, pattern)` - активные подключения

### Этап 5: VLESS Link Generator (1 час)
- `internal/vless/generator.go`
- Генерация ссылок:
```
vless://UUID@HOST:PORT?type=tcp&security=reality&pbk=PUBLIC_KEY&fp=chrome&sni=github.com&sid=SHORT_ID&spx=%2F&flow=xtls-rprx-vision#NAME
```

### Этап 6: Database Repository (3 часа)
- `internal/repository/server/repository.go`:
  - `CreateServer`, `GetServer`, `ListServers`, `UpdateServer`, `DeleteServer`
  - `IncrementCurrentUsers`, `DecrementCurrentUsers`
- `internal/repository/xrayuser/repository.go`:
  - `CreateVPNUser`, `GetVPNUser`, `DeleteVPNUser`
  - `GetByUserId`, `GetBySubscriptionId`
- `internal/repository/connection/repository.go`:
  - `UpsertConnection`, `GetActiveConnections`, `CleanupStaleConnections`
  - `CountActiveDevices`

### Этап 7: Business Logic (5 часов)
- `internal/service/xray/service.go`
- Логика создания пользователя:
  1. Генерировать UUID
  2. Создать email: `user_{user_id}_{subscription_id}`
  3. Получить все активные серверы
  4. Добавить пользователя на каждый сервер через Xray API
  5. Сохранить в БД
  6. Сгенерировать VLESS ссылки для всех серверов
- Логика удаления:
  1. Получить все серверы
  2. Удалить пользователя с каждого сервера через Xray API
  3. Удалить из БД
- Логика контроля лимита:
  1. Получить активные подключения из БД
  2. Сравнить с max_devices из подписки
  3. Вернуть can_connect

### Этап 8: Active Connections Collector (2 часа)
- `internal/collector/collector.go`
- Cron job (каждые 5 минут):
  1. Получить всех Xray пользователей
  2. Для каждого сервера запросить Stats API
  3. Парсить активные подключения (по IP)
  4. Обновить active_connections в БД
  5. Удалить старые (>10 минут)

### Этап 9: gRPC API (2 часа)
- `internal/api/vpn.v1/api.go`
- Реализация всех методов
- Обработка ошибок
- Логирование

### Этап 10: Application Setup (1 час)
- `internal/app/app.go`
- Инициализация DB, gRPC сервера
- Запуск cron job для collector
- Graceful shutdown

### Этап 11: Миграции (30 минут)
- `migrations/001_create_vpn_servers.up.sql`
- `migrations/002_create_vpn_users.up.sql`
- `migrations/003_create_active_connections.up.sql`

### Этап 12: Seed данные (1 час)
- `seeds/001_vpn_servers.sql`
- Добавить тестовые серверы с Reality конфигурацией

### Этап 13: Тестирование (3 часа)
- Unit тесты для VLESS генератора
- Unit тесты для Xray API клиента
- Integration тесты с БД
- Тестирование через grpcurl

### Этап 14: Документация (30 минут)
- `docs/services/xray-service.md`
- Примеры VLESS ссылок
- Troubleshooting

## Оценка времени

**Итого: 24 часа (≈ 3 рабочих дня)**

## Зависимости

### Go библиотеки:
- `google.golang.org/grpc` - gRPC клиент/сервер
- `github.com/jackc/pgx/v5` - PostgreSQL
- `github.com/google/uuid` - UUID генерация
- `github.com/robfig/cron/v3` - Cron jobs
- `go.uber.org/zap` - Логирование

### Внешние зависимости:
- PostgreSQL 15+
- Xray-core с включенным API

## Xray API Setup

На каждом Xray сервере нужно добавить в config.json:

```json
{
  "api": {
    "tag": "api",
    "services": [
      "HandlerService",
      "StatsService"
    ]
  },
  "policy": {
    "levels": {
      "0": {
        "statsUserUplink": true,
        "statsUserDownlink": true
      }
    },
    "system": {
      "statsInboundUplink": true,
      "statsInboundDownlink": true
    }
  },
  "inbounds": [
    {
      "listen": "127.0.0.1",
      "port": 10085,
      "protocol": "dokodemo-door",
      "settings": {
        "address": "127.0.0.1"
      },
      "tag": "api"
    }
  ],
  "routing": {
    "rules": [
      {
        "inboundTag": ["api"],
        "outboundTag": "api",
        "type": "field"
      }
    ]
  }
}
```

## Примеры использования

### Создание Xray пользователя:
```go
resp, err := xrayClient.CreateVPNUser(ctx, &xray.CreateVPNUserRequest{
    UserId: 123,
    SubscriptionId: 456,
})
// resp.VPNUser - информация о пользователе
// resp.VlessLinks - VLESS ссылки для всех серверов
```

### Получение VLESS ссылок:
```go
resp, err := xrayClient.GetVLESSLinks(ctx, &xray.GetVLESSLinksRequest{
    VPNUserId: 789,
})
// resp.VlessLinks[0].Link - vless://UUID@ru.example.com:443?...
// resp.VlessLinks[1].Link - vless://UUID@de.example.com:443?...
```

### Проверка лимита:
```go
resp, err := xrayClient.CheckDeviceLimit(ctx, &xray.CheckDeviceLimitRequest{
    VPNUserId: 789,
})
// resp.CanConnect - true/false
// resp.CurrentDevices - 2
// resp.MaxDevices - 3
```

## Мониторинг

### Метрики:
- `vpn_users_total` - количество пользователей
- `vpn_active_connections_total` - активные подключения
- `vpn_device_limit_exceeded_total` - превышения лимита

### Логи:
- Создание/удаление пользователей
- Ошибки Xray API
- Превышения лимита устройств

## Troubleshooting

### Xray API недоступен:
- Проверить что Xray запущен
- Проверить что API включен в config.json
- Проверить порт 10085

### Пользователь не может подключиться:
- Проверить лимит устройств
- Проверить что пользователь добавлен на сервер
- Проверить VLESS ссылку
