# Channel Bonus Feature

## Описание
Пользователи получают +3 дня к активной подписке за подписку на Telegram канал @maydavpn.

## Реализация

### Backend

#### 1. База данных
Миграция `1777186349_add_channel_bonus.up.sql`:
- `users.channel_bonus_claimed` (BOOLEAN) - флаг получения бонуса
- `users.channel_bonus_claimed_at` (TIMESTAMP) - дата получения

#### 2. Subscription Service
**Proto** (`subscription/v1/subscription.proto`):
```protobuf
rpc ClaimChannelBonus(ClaimChannelBonusRequest) returns (ClaimChannelBonusResponse);
```

**Логика**:
1. Проверка флага `channel_bonus_claimed` (идемпотентность)
2. Поиск активной подписки (status IN ('active', 'trial'))
3. Продление на 3 дня: `expires_at = expires_at + INTERVAL '3 days'`
4. Установка флага `channel_bonus_claimed = true`

**Ответы**:
- `success=true` - бонус начислен
- `already_claimed=true` - уже получал
- `no_active_subscription=true` - нет активной подписки

#### 3. Gateway API
**Endpoints** (требуют JWT):

**POST /api/v1/bonus/check-subscription**
```json
Request: {"user_id": 164015255}
Response: {"subscribed": true}
```

**POST /api/v1/bonus/claim**
```json
Request: {"user_id": 164015255}
Response: {
  "success": true,
  "message": "Бонус начислен! +3 дня к подписке",
  "subscription": {...}
}
```

**Проверка подписки**:
- Telegram Bot API: `getChatMember(@maydavpn, user_id)`
- Статусы: `creator`, `administrator`, `member` = подписан
- Статусы: `left`, `kicked` = не подписан

### Frontend (Telegram Bot)

#### Команда /bonus
Отправляет сообщение с inline кнопками:

```
🎁 Получите +3 дня к подписке!

Подпишитесь на наш канал и получите бонус:

[📢 Подписаться на канал] [✅ Проверить подписку]
```

**Inline кнопки**:
1. **"📢 Подписаться на канал"**
   - `url: https://t.me/maydavpn`
   - Открывает канал в Telegram

2. **"✅ Проверить подписку"**
   - `callback_data: claim_bonus`
   - Вызывает Gateway API `/bonus/claim`

**Callback обработка**:
```javascript
if (callback_query.data === 'claim_bonus') {
  // 1. Проверить подписку через Gateway
  // 2. Если подписан → начислить бонус
  // 3. Показать результат (answerCallbackQuery + editMessageText)
}
```

## Переменные окружения

**Gateway** (`.env`):
```bash
TELEGRAM_BOT_TOKEN=8511824444:AAFCtnMjG1n_mfv2OsXgAvHQtddWxp9Lnf4
TELEGRAM_CHANNEL_USERNAME=@maydavpn
```

## Тестирование

### 1. Проверка подписки через Bot API
```bash
curl "https://api.telegram.org/bot<TOKEN>/getChatMember?chat_id=@maydavpn&user_id=<USER_ID>"
```

### 2. Проверка через Gateway API
```bash
curl -X POST http://localhost:8081/api/v1/bonus/check-subscription \
  -H "Authorization: Bearer <JWT>" \
  -H "Content-Type: application/json" \
  -d '{"user_id": 164015255}'
```

### 3. Начисление бонуса
```bash
curl -X POST http://localhost:8081/api/v1/bonus/claim \
  -H "Authorization: Bearer <JWT>" \
  -H "Content-Type: application/json" \
  -d '{"user_id": 164015255}'
```

## Ограничения

1. **Один раз на пользователя** - флаг `channel_bonus_claimed` предотвращает повторное получение
2. **Только для активных подписок** - триал и платные подписки
3. **Требует подписку на канал** - проверка через Telegram Bot API

## TODO

- [ ] Telegram bot handler для команды /bonus
- [ ] Inline кнопки и callback обработка
- [ ] Тестирование end-to-end
- [ ] Добавить в Mini App UI (опционально)
