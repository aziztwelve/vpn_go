# 04. Referral Service - Спецификация

## Описание

Сервис реферальной программы. Генерация уникальных ссылок, отслеживание переходов, начисление бонусов (дни подписки для обычных пользователей, деньги на баланс для партнёров).

## Требования

### Функциональные:
- Генерация уникальной реферальной ссылки для пользователя
- Регистрация перехода по ссылке (клик)
- Связывание пригласителя и приглашённого
- Начисление бонусов при регистрации (+3 дня)
- Начисление бонусов при покупке (30% для партнёров)
- Применение бонусов (продление подписки, пополнение баланса)
- Статистика рефералов (количество, заработок)

### Нефункциональные:
- Уникальность токенов (8 символов, base62)
- Время генерации ссылки < 50ms
- Атомарность начисления бонусов

## Схема базы данных

```sql
CREATE TABLE referral_links (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    token VARCHAR(50) NOT NULL UNIQUE,
    clicks INT DEFAULT 0,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE referral_relationships (
    inviter_id BIGINT NOT NULL REFERENCES users(id),
    invited_id BIGINT NOT NULL REFERENCES users(id),
    status VARCHAR(20) DEFAULT 'registered',
    created_at TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (inviter_id, invited_id)
);

CREATE TABLE referral_bonuses (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    invited_user_id BIGINT NOT NULL REFERENCES users(id),
    bonus_type VARCHAR(20) NOT NULL,
    days_amount INT,
    balance_amount DECIMAL(12,2),
    is_applied BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMPTZ DEFAULT NOW()
);
```

## API (gRPC)

```protobuf
service ReferralService {
  rpc GetOrCreateReferralLink(GetOrCreateReferralLinkRequest) returns (GetOrCreateReferralLinkResponse);
  rpc RegisterClick(RegisterClickRequest) returns (RegisterClickResponse);
  rpc RegisterReferral(RegisterReferralRequest) returns (RegisterReferralResponse);
  rpc CreateBonus(CreateBonusRequest) returns (CreateBonusResponse);
  rpc ApplyBonus(ApplyBonusRequest) returns (ApplyBonusResponse);
  rpc GetReferralStats(GetReferralStatsRequest) returns (GetReferralStatsResponse);
}
```

## План реализации

1. **Структура проекта** (1 час)
2. **Proto определения** (30 минут)
3. **Token Generator** (1 час)
   - Генерация уникальных токенов (base62, 8 символов)
4. **Database Repository** (2 часа)
5. **Business Logic** (3 часа)
   - Генерация ссылки
   - Регистрация клика
   - Связывание пользователей
   - Начисление бонусов (дни/деньги)
   - Применение бонусов
6. **gRPC API** (2 часа)
7. **Тестирование** (2 часа)
8. **Документация** (30 минут)

**Итого: 12 часов (≈ 1.5 рабочих дня)**

## Логика начисления бонусов

### При регистрации реферала:
- Обычный пользователь: +3 дня к текущей подписке
- Партнёр: ничего (ждём покупки)

### При покупке рефералом:
- Обычный пользователь: +3 дня к текущей подписке
- Партнёр: 30% от суммы платежа на баланс

## Примеры

### Получение реферальной ссылки:
```go
resp, err := referralClient.GetOrCreateReferralLink(ctx, &referral.GetOrCreateReferralLinkRequest{
    UserId: 123,
})
// resp.Link - "https://t.me/extravpn_bot?start=EXSPS3AV"
// resp.Token - "EXSPS3AV"
```

### Регистрация реферала:
```go
resp, err := referralClient.RegisterReferral(ctx, &referral.RegisterReferralRequest{
    InviterId: 123,
    InvitedId: 456,
})
// Создаётся связь, начисляется бонус +3 дня
```

### Статистика:
```go
resp, err := referralClient.GetReferralStats(ctx, &referral.GetReferralStatsRequest{
    UserId: 123,
})
// resp.TotalReferrals - количество рефералов
// resp.TotalEarnings - заработок (для партнёров)
// resp.PendingBonuses - неприменённые бонусы
```
