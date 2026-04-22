# 01. Auth Service - Спецификация

## Описание

Сервис авторизации и управления пользователями. Валидирует Telegram initData, создаёт/обновляет пользователей, управляет ролями и выдаёт JWT токены для внутренней авторизации между микросервисами.

## Требования

### Функциональные:
- Валидация Telegram WebApp initData (проверка подписи HMAC-SHA256)
- Создание нового пользователя при первом входе
- Обновление данных пользователя (username, first_name, last_name, photo_url)
- Управление ролями (user → partner, admin)
- Генерация JWT токенов для внутренней авторизации
- Проверка JWT токенов
- Получение информации о пользователе
- Бан/разбан пользователей

### Нефункциональные:
- Время валидации initData < 100ms
- Поддержка 1000+ RPS
- Логирование всех попыток авторизации
- Graceful shutdown

## Схема базы данных

Использует таблицу `users`:

```sql
CREATE TABLE users (
    id BIGSERIAL PRIMARY KEY,
    telegram_id BIGINT NOT NULL UNIQUE,
    username VARCHAR(255),
    first_name VARCHAR(255),
    last_name VARCHAR(255),
    photo_url TEXT,
    language_code VARCHAR(10) DEFAULT 'ru',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_active_at TIMESTAMPTZ,
    is_banned BOOLEAN NOT NULL DEFAULT FALSE,
    role VARCHAR(50) NOT NULL DEFAULT 'user',
    balance DECIMAL(12,2) NOT NULL DEFAULT 0
);
```

## API (gRPC)

### Proto Definition

```protobuf
syntax = "proto3";

package auth.v1;

option go_package = "github.com/vpn/shared/pkg/proto/auth/v1";

service AuthService {
  // Валидация Telegram initData и создание/обновление пользователя
  rpc ValidateTelegramUser(ValidateTelegramUserRequest) returns (ValidateTelegramUserResponse);
  
  // Получение информации о пользователе
  rpc GetUser(GetUserRequest) returns (GetUserResponse);
  
  // Обновление роли пользователя (только для админов)
  rpc UpdateUserRole(UpdateUserRoleRequest) returns (UpdateUserRoleResponse);
  
  // Бан/разбан пользователя (только для админов)
  rpc BanUser(BanUserRequest) returns (BanUserResponse);
  
  // Проверка JWT токена
  rpc VerifyToken(VerifyTokenRequest) returns (VerifyTokenResponse);
}

message ValidateTelegramUserRequest {
  string init_data = 1; // Telegram WebApp initData
}

message ValidateTelegramUserResponse {
  User user = 1;
  string jwt_token = 2; // JWT для внутренней авторизации
}

message GetUserRequest {
  int64 user_id = 1;
}

message GetUserResponse {
  User user = 1;
}

message UpdateUserRoleRequest {
  int64 user_id = 1;
  string role = 2; // user, partner, admin
}

message UpdateUserRoleResponse {
  User user = 1;
}

message BanUserRequest {
  int64 user_id = 1;
  bool is_banned = 2;
}

message BanUserResponse {
  User user = 1;
}

message VerifyTokenRequest {
  string token = 1;
}

message VerifyTokenResponse {
  int64 user_id = 1;
  string role = 2;
  bool is_valid = 3;
}

message User {
  int64 id = 1;
  int64 telegram_id = 2;
  string username = 3;
  string first_name = 4;
  string last_name = 5;
  string photo_url = 6;
  string language_code = 7;
  string role = 8;
  bool is_banned = 9;
  string balance = 10; // Decimal as string
  string created_at = 11;
  string updated_at = 12;
  string last_active_at = 13;
}
```

## План реализации

### Этап 1: Структура проекта (1 час)
- Создать директории `services/auth-service/{cmd,internal,migrations}`
- Создать `go.mod`
- Создать `.env` файл
- Создать `main.go`

### Этап 2: Proto и генерация (30 минут)
- Создать `shared/proto/auth/v1/auth.proto`
- Сгенерировать Go код через `task proto:gen`
- Проверить импорты

### Этап 3: Конфигурация (30 минут)
- Создать `internal/config/config.go`
- Переменные окружения (gRPC порт, DB, JWT secret, Telegram bot token)
- Валидация конфига

### Этап 4: Database Repository (2 часа)
- `internal/repository/user/repository.go`
- Методы:
  - `CreateUser(ctx, telegramID, username, firstName, lastName, photoURL, langCode) → User`
  - `GetUserByTelegramID(ctx, telegramID) → User`
  - `GetUserByID(ctx, userID) → User`
  - `UpdateUser(ctx, user) → User`
  - `UpdateUserRole(ctx, userID, role) → User`
  - `BanUser(ctx, userID, isBanned) → User`
  - `UpdateLastActive(ctx, userID) → error`

### Этап 5: Business Logic (3 часа)
- `internal/service/auth/service.go`
- Валидация Telegram initData:
  - Парсинг query string
  - Проверка hash через HMAC-SHA256
  - Проверка auth_date (не старше 24 часов)
- Генерация JWT токенов (библиотека `golang-jwt/jwt`)
- Проверка JWT токенов
- Логика создания/обновления пользователя

### Этап 6: gRPC API (2 часа)
- `internal/api/auth/v1/api.go`
- Реализация всех методов из proto
- Обработка ошибок (gRPC status codes)
- Логирование запросов

### Этап 7: Application Setup (1 час)
- `internal/app/app.go`
- Инициализация DB, gRPC сервера
- Graceful shutdown
- Health check

### Этап 8: Миграции (30 минут)
- `migrations/001_create_users.up.sql`
- `migrations/001_create_users.down.sql`
- Интеграция с `golang-migrate`

### Этап 9: Тестирование (2 часа)
- Unit тесты для валидации Telegram initData
- Unit тесты для JWT генерации/проверки
- Integration тесты с реальной БД (testcontainers)
- Тестирование через grpcurl

### Этап 10: Документация (30 минут)
- `docs/services/auth-service.md`
- Примеры использования
- Troubleshooting

## Оценка времени

- Этап 1: 1 час
- Этап 2: 30 минут
- Этап 3: 30 минут
- Этап 4: 2 часа
- Этап 5: 3 часа
- Этап 6: 2 часа
- Этап 7: 1 час
- Этап 8: 30 минут
- Этап 9: 2 часа
- Этап 10: 30 минут

**Итого: 13 часов (≈ 2 рабочих дня)**

## Зависимости

### Go библиотеки:
- `google.golang.org/grpc` - gRPC сервер
- `github.com/jackc/pgx/v5` - PostgreSQL драйвер
- `github.com/golang-jwt/jwt/v5` - JWT токены
- `go.uber.org/zap` - Логирование
- `github.com/joho/godotenv` - .env файлы

### Внешние зависимости:
- PostgreSQL 15+
- Telegram Bot Token (для валидации initData)

## Безопасность

### Валидация Telegram initData:
1. Парсим query string в map
2. Извлекаем `hash`
3. Создаём data_check_string (все параметры кроме hash, отсортированные по ключу)
4. Вычисляем secret_key = HMAC-SHA256(bot_token, "WebAppData")
5. Вычисляем hash = HMAC-SHA256(secret_key, data_check_string)
6. Сравниваем с переданным hash

### JWT токены:
- Алгоритм: HS256
- Payload: `{user_id, role, exp}`
- TTL: 7 дней
- Secret хранится в переменной окружения

## Примеры использования

### Валидация пользователя:
```go
resp, err := authClient.ValidateTelegramUser(ctx, &auth.ValidateTelegramUserRequest{
    InitData: "query_id=...&user=...&hash=...",
})
// resp.User - информация о пользователе
// resp.JwtToken - токен для дальнейших запросов
```

### Проверка токена:
```go
resp, err := authClient.VerifyToken(ctx, &auth.VerifyTokenRequest{
    Token: "eyJhbGciOiJIUzI1NiIs...",
})
// resp.IsValid - валиден ли токен
// resp.UserId - ID пользователя
// resp.Role - роль пользователя
```

## Мониторинг

### Метрики:
- `auth_validate_telegram_total` - количество валидаций
- `auth_validate_telegram_errors` - количество ошибок валидации
- `auth_jwt_generate_total` - количество сгенерированных токенов
- `auth_jwt_verify_total` - количество проверок токенов

### Логи:
- Все попытки авторизации (успешные и неуспешные)
- Изменения ролей пользователей
- Баны/разбаны

## Troubleshooting

### Ошибка валидации initData:
- Проверить что Telegram Bot Token правильный
- Проверить что initData не старше 24 часов
- Проверить что hash совпадает

### JWT токен невалиден:
- Проверить что JWT_SECRET одинаковый на всех сервисах
- Проверить что токен не истёк (exp)
