# 06. Traffic Service - Спецификация

## Описание

Сервис учёта трафика. Собирает статистику использования VPN с WireGuard серверов, агрегирует данные, предоставляет статистику пользователям.

## Требования

### Функциональные:
- Запись трафика устройства (bytes_rx, bytes_tx)
- Получение статистики трафика пользователя (за период)
- Получение статистики трафика устройства
- Агрегация данных (по дням, месяцам)
- Очистка старых логов (> 90 дней)

### Нефункциональные:
- Batch запись (группировка записей)
- Время записи < 10ms
- Поддержка 10000+ записей в секунду
- Асинхронная обработка

## Схема базы данных

```sql
CREATE TABLE traffic_logs (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    device_id BIGINT NOT NULL REFERENCES devices(id),
    bytes_rx BIGINT DEFAULT 0,
    bytes_tx BIGINT DEFAULT 0,
    recorded_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_traffic_logs_user_id ON traffic_logs(user_id);
CREATE INDEX idx_traffic_logs_device_id ON traffic_logs(device_id);
CREATE INDEX idx_traffic_logs_recorded_at ON traffic_logs(recorded_at);
```

## API (gRPC)

```protobuf
service TrafficService {
  // Записать трафик (batch)
  rpc RecordTraffic(RecordTrafficRequest) returns (RecordTrafficResponse);
  
  // Получить статистику пользователя
  rpc GetUserTraffic(GetUserTrafficRequest) returns (GetUserTrafficResponse);
  
  // Получить статистику устройства
  rpc GetDeviceTraffic(GetDeviceTrafficRequest) returns (GetDeviceTrafficResponse);
  
  // Очистить старые логи (cron job)
  rpc CleanupOldLogs(CleanupOldLogsRequest) returns (CleanupOldLogsResponse);
}

message RecordTrafficRequest {
  repeated TrafficRecord records = 1;
}

message TrafficRecord {
  int64 device_id = 1;
  int64 bytes_rx = 2;
  int64 bytes_tx = 3;
}

message GetUserTrafficRequest {
  int64 user_id = 1;
  string period = 2; // "day", "week", "month"
}

message GetUserTrafficResponse {
  int64 total_rx = 1;
  int64 total_tx = 2;
  repeated DailyTraffic daily = 3;
}

message DailyTraffic {
  string date = 1;
  int64 bytes_rx = 2;
  int64 bytes_tx = 3;
}
```

## План реализации

1. **Структура проекта** (1 час)
2. **Proto определения** (30 минут)
3. **Database Repository** (2 часа)
   - Batch insert для производительности
   - Агрегация по периодам
4. **Business Logic** (2 часа)
   - Batch обработка
   - Агрегация статистики
   - Cleanup старых данных
5. **gRPC API** (1 час)
6. **WireGuard Integration** (2 часа)
   - Скрипт для сбора статистики с WireGuard
   - Периодическая отправка в Traffic Service
7. **Тестирование** (2 часа)
8. **Документация** (30 минут)

**Итого: 11 часов (≈ 1.5 рабочих дня)**

## WireGuard Integration

### Сбор статистики:
```bash
# На каждом WireGuard сервере
wg show all transfer | while read -r line; do
  # Парсим public_key, bytes_rx, bytes_tx
  # Отправляем в Traffic Service через gRPC
done
```

### Cron job:
```bash
# Каждые 5 минут
*/5 * * * * /usr/local/bin/collect-traffic.sh
```

## Примеры

### Запись трафика (batch):
```go
resp, err := trafficClient.RecordTraffic(ctx, &traffic.RecordTrafficRequest{
    Records: []*traffic.TrafficRecord{
        {DeviceId: 1, BytesRx: 1024000, BytesTx: 512000},
        {DeviceId: 2, BytesRx: 2048000, BytesTx: 1024000},
    },
})
```

### Получение статистики:
```go
resp, err := trafficClient.GetUserTraffic(ctx, &traffic.GetUserTrafficRequest{
    UserId: 123,
    Period: "month",
})
// resp.TotalRx - всего принято
// resp.TotalTx - всего отправлено
// resp.Daily - статистика по дням
```

## Оптимизация

### Batch запись:
- Группировка записей по 100-1000 штук
- Использование `COPY` вместо `INSERT`

### Партиционирование:
```sql
-- Партиционирование по месяцам
CREATE TABLE traffic_logs_2026_04 PARTITION OF traffic_logs
FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
```

### Cleanup:
```sql
-- Удаление логов старше 90 дней
DELETE FROM traffic_logs WHERE recorded_at < NOW() - INTERVAL '90 days';
```
