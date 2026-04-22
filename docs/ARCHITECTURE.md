# ExtraVPN - Архитектура системы

## 📊 Обзор

VPN сервис с Telegram Mini App интерфейсом, реферальной программой и партнёрскими выплатами.

## 🏗️ Архитектура

```
┌─────────────────────────────────────────────────────────────┐
│                    Telegram Mini App                         │
│                      (vpn_next)                              │
│  - Авторизация через Telegram WebApp                        │
│  - Выбор тарифа и устройств                                 │
│  - Управление устройствами                                   │
│  - Реферальная программа                                     │
└────────────────────┬────────────────────────────────────────┘
                     │ HTTPS
                     ▼
┌─────────────────────────────────────────────────────────────┐
│                   API Gateway (Go)                           │
│                   Port: 8081                                 │
│  - HTTP → gRPC маршрутизация                                │
│  - Валидация Telegram initData                              │
│  - Rate limiting                                             │
│  - CORS                                                      │
└────────────────────┬────────────────────────────────────────┘
                     │ gRPC
                     ▼
┌─────────────────────────────────────────────────────────────┐
│                   Microservices                              │
│                                                              │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐     │
│  │ Auth Service │  │Subscription  │  │ VPN Service  │     │
│  │ Port: 50060  │  │ Port: 50061  │  │ Port: 50062  │     │
│  └──────────────┘  └──────────────┘  └──────────────┘     │
│                                                              │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐     │
│  │ Payment Svc  │  │Referral Svc  │  │ Admin Svc    │     │
│  │ Port: 50063  │  │ Port: 50064  │  │ Port: 50065  │     │
│  └──────────────┘  └──────────────┘  └──────────────┘     │
└────────────────────┬────────────────────────────────────────┘
                     │
                     ▼
┌─────────────────────────────────────────────────────────────┐
│                   PostgreSQL 15+                             │
│  - 12 таблиц (users, subscriptions, devices, payments...)   │
└─────────────────────────────────────────────────────────────┘
                     │
                     ▼
┌─────────────────────────────────────────────────────────────┐
│              WireGuard Servers (VPS)                         │
│  - Россия, Европа, Азия, США                                │
│  - Управление через API                                      │
│  - Мониторинг трафика                                        │
└─────────────────────────────────────────────────────────────┘
```

## 🎯 Микросервисы

### 1. Auth Service (50060)
**Ответственность:**
- Валидация Telegram initData
- Создание/обновление пользователей
- Управление ролями (user/admin/partner)
- JWT токены для внутренней авторизации

**API:**
- `ValidateTelegramUser(initData) → User`
- `GetUser(userId) → User`
- `UpdateUserRole(userId, role) → User`

### 2. VPN Service (50062)
**Ответственность:**
- Управление Xray серверов (через Xray API)
- Генерация UUID для пользователей
- Создание/удаление пользователей в Xray
- Генерация VLESS ссылок для подключения
- Контроль лимита одновременных подключений
- Сбор активных подключений

**API:**
- `ListServers() → []Server`
- `CreateXrayUser(userId, subscriptionId) → XrayUser + VLESSLinks`
- `GetVLESSLinks(xrayUserId) → []VLESSLink (по серверам)`
- `DeleteXrayUser(xrayUserId) → Success`
- `GetActiveConnections(xrayUserId) → []Connection`
- `CheckDeviceLimit(xrayUserId) → CanConnect`

### 3. Payment Service (50063)
**Ответственность:**
- Интеграция с ЮKassa/Stripe
- Создание платежей
- Webhook обработка
- История транзакций

**API:**
- `CreatePayment(userId, planId, devices) → PaymentURL`
- `HandleWebhook(provider, data) → Success`
- `GetPaymentHistory(userId) → []Payment`

### 4. Referral Service (50064)
**Ответственность:**
- Генерация реферальных ссылок
- Отслеживание переходов
- Начисление бонусов (дни/деньги)
- Статистика рефералов

**API:**
- `GetOrCreateReferralLink(userId) → ReferralLink`
- `RegisterReferral(inviterId, invitedId) → Success`
- `ApplyBonus(userId, bonusId) → Success`
- `GetReferralStats(userId) → Stats`

### 5. Admin Service (50065)
**Ответственность:**
- Управление пользователями
- Управление серверами
- Обработка заявок на вывод
- Статистика и аналитика

**API:**
- `ListUsers(filters) → []User`
- `BanUser(userId) → Success`
- `ListWithdrawalRequests() → []Request`
- `ProcessWithdrawal(requestId, status) → Success`
- `GetDashboardStats() → Stats`

### 6. Subscription Service (50061)
**Ответственность:**
- Тарифы и цены за дополнительные устройства
- Создание/продление подписок
- Проверка активности подписки (used by VPN Service)

**API:**
- `ListPlans() → []Plan`
- `GetPricing(planId, devices) → Price`
- `CreateSubscription(userId, planId, devices) → Subscription`
- `GetActiveSubscription(userId) → Subscription`
- `ExtendSubscription(subscriptionId, days) → Subscription`

## 💾 База данных

### Основные таблицы:
1. **users** - пользователи (Telegram ID, роль, баланс)
2. **vpn_servers** - WireGuard серверы (локации, ключи)
3. **subscription_plans** - тарифы (1/3/6/12 месяцев)
4. **device_addon_pricing** - цены на доп. устройства
5. **subscriptions** - активные подписки
6. **devices** - WireGuard конфигурации
7. **traffic_logs** - учёт трафика
8. **payments** - история платежей
9. **referral_links** - реферальные ссылки
10. **referral_relationships** - связи пользователей
11. **referral_bonuses** - начисленные бонусы
12. **withdrawal_requests** - заявки на вывод

## 🔐 Безопасность

### Telegram авторизация:
1. Клиент получает `initData` от Telegram
2. Gateway валидирует подпись через Auth Service
3. Создаётся/обновляется пользователь
4. Выдаётся JWT токен для дальнейших запросов

### Xray UUID:
- UUID генерируется один раз для пользователя
- Один UUID используется на всех серверах
- Email формат: `user_{user_id}_{subscription_id}`
- Контроль лимита устройств через active_connections

## 💰 Монетизация

### Тарифы:
- 1 месяц: 199₽ (2 устройства)
- 3 месяца: 550₽ (2 устройства)
- 6 месяцев: 1100₽ (2 устройства)
- 12 месяцев: 1999₽ (2 устройства)

### Дополнительные устройства:
- 3 устройства: +90₽
- 4 устройства: +180₽
- 5 устройств: +270₽
- и т.д.

### Реферальная программа:
- **Обычные пользователи:** +3 дня за каждого друга
- **Партнёры:** 30% от платежей рефералов на баланс

## 🚀 Deployment

### Development:
```bash
# Backend
cd vpn_go
task dev:gateway

# Frontend
cd vpn_next
npm run dev
```

### Production:
```bash
# Docker Compose
docker-compose up -d

# Или через Taskfile
task build-and-run
```

## 📈 Масштабирование

### Горизонтальное:
- Каждый микросервис может масштабироваться независимо
- Load balancer перед Gateway
- Read replicas для PostgreSQL

### Вертикальное:
- Добавление новых VPN серверов в разных локациях
- Увеличение ресурсов существующих серверов

## 🔄 Workflow

### Покупка подписки:
1. Пользователь выбирает тариф и количество устройств
2. Payment Service создаёт платёж в ЮKassa
3. Пользователь оплачивает
4. Webhook → Payment Service → создаётся subscription
5. Referral Service начисляет бонусы пригласителю

### Создание Xray пользователя:
1. Пользователь покупает подписку
2. VPN Service генерирует UUID
3. Добавляет пользователя на все активные серверы через Xray API
4. Возвращает VLESS ссылки для всех локаций
5. Пользователь импортирует ссылки в клиент (V2rayNG, Shadowrocket и т.д.)

### Партнёрские выплаты:
1. Партнёр создаёт заявку на вывод
2. Admin Service проверяет баланс
3. Админ одобряет/отклоняет заявку
4. Выплата производится вручную
5. Баланс партнёра уменьшается

## 📝 TODO

- [ ] Реализовать все 6 микросервисов
- [ ] Интеграция с ЮKassa
- [ ] Xray API интеграция (gRPC)
- [ ] Контроль лимита устройств
- [ ] Админ панель (отдельный Next.js проект?)
- [ ] Мониторинг (Prometheus + Grafana)
- [ ] CI/CD pipeline
- [ ] Документация API (Swagger)
