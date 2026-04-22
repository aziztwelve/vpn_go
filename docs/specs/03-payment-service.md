# 03. Payment Service - Спецификация

## Описание

Сервис обработки платежей. Интеграция с ЮKassa/Stripe, создание платежей, обработка webhook'ов, создание подписок после успешной оплаты.

## Требования

### Функциональные:
- Создание платежа (генерация URL для оплаты)
- Обработка webhook от платёжных систем
- Создание подписки после успешной оплаты
- История платежей пользователя
- Возвраты (refunds)
- Проверка статуса платежа

### Нефункциональные:
- Идемпотентность webhook'ов
- Время создания платежа < 200ms
- Логирование всех транзакций
- Retry механизм для webhook'ов

## Схема базы данных

```sql
CREATE TABLE payments (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    subscription_id BIGINT REFERENCES subscriptions(id),
    amount DECIMAL(10,2) NOT NULL,
    currency VARCHAR(3) DEFAULT 'RUB',
    provider VARCHAR(50) NOT NULL,
    provider_payment_id VARCHAR(255),
    status VARCHAR(20) DEFAULT 'pending',
    metadata JSONB,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    completed_at TIMESTAMPTZ
);

CREATE TABLE subscriptions (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    plan_id INT NOT NULL REFERENCES subscription_plans(id),
    total_devices INT NOT NULL,
    total_price DECIMAL(10,2) NOT NULL,
    started_at TIMESTAMPTZ DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    status VARCHAR(20) DEFAULT 'active',
    created_at TIMESTAMPTZ DEFAULT NOW()
);
```

## API (gRPC)

```protobuf
service PaymentService {
  rpc CreatePayment(CreatePaymentRequest) returns (CreatePaymentResponse);
  rpc HandleWebhook(HandleWebhookRequest) returns (HandleWebhookResponse);
  rpc GetPaymentHistory(GetPaymentHistoryRequest) returns (GetPaymentHistoryResponse);
  rpc GetPaymentStatus(GetPaymentStatusRequest) returns (GetPaymentStatusResponse);
}
```

## План реализации

1. **Структура проекта** (1 час)
2. **Proto определения** (30 минут)
3. **ЮKassa SDK интеграция** (2 часа)
4. **Database Repository** (2 часа)
5. **Business Logic** (3 часа)
   - Создание платежа
   - Обработка webhook
   - Создание подписки
   - Начисление реферальных бонусов
6. **gRPC API** (2 часа)
7. **Webhook endpoint** (1 час)
8. **Тестирование** (2 часа)
9. **Документация** (30 минут)

**Итого: 14 часов (≈ 2 рабочих дня)**

## Зависимости

- ЮKassa Go SDK
- PostgreSQL
- Referral Service (для начисления бонусов)

## Безопасность

- Проверка подписи webhook'ов
- Идемпотентность (проверка по provider_payment_id)
- Логирование всех транзакций

## Примеры

### Создание платежа:
```go
resp, err := paymentClient.CreatePayment(ctx, &payment.CreatePaymentRequest{
    UserId: 123,
    PlanId: 1,
    TotalDevices: 3,
})
// resp.PaymentUrl - URL для оплаты
// resp.PaymentId - ID платежа
```

### Обработка webhook:
```go
resp, err := paymentClient.HandleWebhook(ctx, &payment.HandleWebhookRequest{
    Provider: "yookassa",
    Payload: webhookData,
})
// Создаётся подписка, начисляются бонусы
```
