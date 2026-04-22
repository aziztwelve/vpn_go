# 01. MVP План — Варианты запуска

**Дата:** 2026-04-22  
**Статус:** 🟢 Утверждён — **Вариант C (Коммерческий MVP)** (2026-04-22)  
**Автор:** Devin + aziz  
**Реализация:** [02-mvp-c-implementation.md](./02-mvp-c-implementation.md)

---

## 🎯 Цель документа

После глубокого изучения текущего состояния `vpn_go` предложить **3 реалистичных варианта MVP** с разной глубиной готовности. Выбрать один — и по нему составить пошаговую задачу.

---

## 📊 Что уже реально работает (снимок на 2026-04-22)

### ✅ Backend (живой код + БД)

| Сервис | Порт | Статус | Что есть |
|---|---|---|---|
| Auth Service | 50051 | ✅ Работает | Telegram initData (HMAC-SHA256), JWT (168h), users CRUD, бан/роли |
| Subscription Service | 50056 | ✅ Работает | 4 плана (1/3/6/12 мес), device addon pricing, подписки, extend/cancel |
| VPN Service | 50057 | 🟡 Полу-готов | UUID генерация, VLESS link генерация, 4 mock-сервера, active_connections |
| Gateway | 8081 | ✅ Работает | HTTP → gRPC, CORS, chi router |
| PostgreSQL | 5432 | ✅ | 9 таблиц с FK |

### 📦 gRPC API (proto)

- **Auth:** `ValidateTelegramUser`, `GetUser`, `UpdateUserRole`, `BanUser`, `VerifyToken`
- **Subscription:** `ListPlans`, `GetDevicePricing`, `CreateSubscription`, `GetActiveSubscription`, `ExtendSubscription`, `CancelSubscription`, `CheckSubscriptionActive`, `GetSubscriptionHistory`
- **VPN:** `CreateVPNUser`, `GetVPNUser`, `GetVLESSLink`, `ListServers`, `GetActiveConnections`, `DisconnectDevice`

### 🌐 HTTP Routes (Gateway `/api/v1`)

```
POST   /auth/validate
GET    /auth/users/{userId}
GET    /subscriptions/plans
GET    /subscriptions/plans/{planId}/pricing
GET    /subscriptions/active
POST   /subscriptions
GET    /subscriptions/history
GET    /vpn/servers
GET    /vpn/servers/{serverId}/link
GET    /vpn/connections
```

### 🗄️ База данных (9 таблиц, реально существуют)

```
users                    subscription_plans
vpn_servers              device_addon_pricing
vpn_users                subscriptions
active_connections       payments (пустая)
traffic_logs (пустая)    referral_links (нет)
                         referral_bonuses (нет)
                         withdrawal_requests (нет)
```

---

## 🚨 Критические разрывы (gap analysis)

### 🔴 Блокеры для реального продукта

1. **Xray API не интегрирован.** VLESS ссылки генерируются из записей в БД с `test_public_key_us`, `test_private_key_us` — это **фейковые ключи Reality**. Клиент по такой ссылке никуда не подключится. Серверы `us.vpn.example.com`, `de.vpn.example.com` — несуществующие домены.
2. **Нет реального Xray VPS.** Ни одного живого сервера с Xray + Reality нет. Всё в БД — декорации.
3. **Нет Payment Service.** Поле `payments` в БД пустое, нет интеграции с ЮKassa/Stripe/Telegram Stars. Подписка создаётся без оплаты (`CreateSubscription` не проверяет платёж).
4. **Device limit не enforced.** Таблица `active_connections` есть, но нет логики "max_devices=3 → 4-е подключение отбивается".

### 🟡 Важное, но не блокирующее

5. **JWT middleware в Gateway отсутствует.** Любой может дёрнуть `/api/v1/subscriptions` без токена — подмешать `userId` в payload.
6. **Frontend не валидирует Telegram initData.** Использует mock-данные, стоит как standalone web-app, не Mini App.
7. **Docker-compose папки пустые** (`deploy/compose/*` — нет yml файлов).
8. **Нет миграционного тула** (goose/migrate) — миграции накатываются руками.
9. **Referral Service, Admin Service, Traffic Service** — только спеки, кода нет.

### 🟢 Косметика

10. Нет Swagger/OpenAPI описания.
11. Нет тестов (unit/integration).
12. Логи не структурированы в JSON.
13. Нет rate limiting.

---

## 🛣️ Три варианта MVP

### 🅰️ Вариант A — "Демо-версия для показа"

> **Цель:** Красивое демо флоу, без реального VPN. Можно показать заказчикам/инвесторам, снять скрины для маркетинга.

**Что делаем:**
1. Починить фронтенд (работает в Telegram Mini App + standalone fallback)
2. Прикрутить JWT middleware в Gateway
3. Telegram initData → JWT flow на клиенте
4. Заглушка Payment Service (возвращает "оплачено" сразу, без настоящего ЮKassa)
5. Добавить 1-2 экрана в админку (admin видит список юзеров)

**Что НЕ делаем:**
- ❌ Xray integration
- ❌ Реальные VPS
- ❌ Реальные платежи
- ❌ Referral, Traffic, полноценный Admin

**Оценка:** 2-3 дня  
**Плюсы:** Быстро. Видно цельный продукт. Хорошо для презентаций.  
**Минусы:** Реальный пользователь не подключится к VPN — это муляж.  
**Риски:** Минимальные.  
**Когда выбирать:** Нужно показать концепт клиенту/инвестору/другу за эту неделю.

---

### 🅱️ Вариант B — "Реальный VPN, ручные продажи"

> **Цель:** Настоящий работающий VPN с одним сервером. Пользователь реально подключается и серфит. Оплата обрабатывается админом вручную.

**Что делаем:**
1. Всё из варианта A (фронт, JWT, initData)
2. **Xray API integration** — модуль `platform/pkg/xray/` с методами `AddUser`, `RemoveUser`, `GetStats` через gRPC Xray API
3. **Один реальный VPS** с Xray + Reality (настроить через Ansible или docker-compose)
4. Убрать mock-серверы из `vpn_servers`, положить реальный
5. **Device limit enforcement** в VPN Service (перед созданием connection → проверка `SELECT COUNT(*) FROM active_connections WHERE vpn_user_id=? AND last_seen > NOW() - INTERVAL '5 min'`)
6. Упрощённая админка: админ вручную активирует подписку кнопкой после получения денег в Telegram/на карту
7. Health-check для Xray сервера (крон каждые 60с обновляет `load_percent`)

**Что НЕ делаем:**
- ❌ Payment Service (ручная активация)
- ❌ Multi-server (пока 1 локация)
- ❌ Referral, Traffic Service

**Оценка:** 5-7 дней  
**Плюсы:** Настоящий VPN. Первый реальный юзер может подключиться. Основа масштабируется.  
**Минусы:** Нужен VPS (финансы: ~5-10$/мес), нужно настроить Xray, нет автоматизации оплаты.  
**Риски:** Xray API может ломаться при рестарте, Reality handshake чувствителен к времени/DNS.  
**Когда выбирать:** Хотим MVP для первых 10-50 реальных пользователей (друзья, early adopters).

---

### 🅲 Вариант C — "Коммерческий MVP"

> **Цель:** Готов к приёму денег от незнакомцев. Автоматическая оплата, реферальная программа, multi-server.

**Что делаем:**
1. Всё из варианта B
2. **Payment Service (50053)** — интеграция с **Telegram Stars** (проще ЮKassa, не нужна регистрация юрлица) или ЮKassa (если есть ИП)
3. Webhook handler для подтверждения оплаты → автоматическое создание subscription + vpn_user
4. **Referral Service (50054)** — базовый: генерация ссылки, трекинг регистраций, бонус +3 дня новому юзеру и пригласителю (без partner-логики с выплатами)
5. 2-3 реальных VPS в разных локациях (США, Европа, Азия)
6. **Admin Service (50055)** — список юзеров, подписок, платежей, возможность бана
7. CI/CD (хотя бы build на push через GitHub Actions)
8. Мониторинг (uptime ping на серверы)

**Что НЕ делаем:**
- ❌ Traffic Service (статистика — отдельная задача)
- ❌ Partner withdrawals (сложная фича)
- ❌ Масштабирование (нет load balancer между серверами)

**Оценка:** 10-14 дней  
**Плюсы:** Готов монетизироваться. Реферальная программа = вирусный рост.  
**Минусы:** Много движущихся частей. Больше мест для багов. Нужна юридическая база (оферта, политика).  
**Риски:** Telegram Stars — комиссия ~30%. ЮKassa требует ИП. Multi-server = multi-head Xray ≠ тривиально.  
**Когда выбирать:** Готовы выкатывать продукт публично, запускать рекламу.

---

## 🎁 Бонус — Вариант D (альтернатива)

> **"Xray-минимум"** — убрать всё лишнее, оставить только Auth + VPN + Telegram Stars. Никаких подписок-планов — одна кнопка "500 Stars = 30 дней VPN". Самый тонкий слой.

**Оценка:** 4-5 дней  
**Когда выбирать:** Хочется максимально быстро выйти в прод с минимумом кода.

---

## 📊 Сравнительная таблица

| Критерий | A (Демо) | B (Реальный VPN) | C (Коммерческий) | D (Минимум) |
|---|:---:|:---:|:---:|:---:|
| Срок | 2-3 дня | 5-7 дней | 10-14 дней | 4-5 дней |
| Реальные подключения | ❌ | ✅ | ✅ | ✅ |
| Автооплата | ❌ | ❌ | ✅ | ✅ (Stars) |
| Рефералка | ❌ | ❌ | ✅ | ❌ |
| Multi-server | ❌ | ❌ | ✅ | ❌ |
| Нужен VPS | ❌ | ✅ (1) | ✅ (2-3) | ✅ (1) |
| Нужна оферта | ❌ | ❌ | ✅ | ✅ |
| Готов к публике | ❌ | 🟡 | ✅ | ✅ |

---

## ✅ Решение

**2026-04-22:** Aziz выбрал **Вариант C — Коммерческий MVP**.

Детальный пошаговый план реализации: **[02-mvp-c-implementation.md](./02-mvp-c-implementation.md)**

---

## 📎 Ссылки

- [ARCHITECTURE.md](../ARCHITECTURE.md) — полная архитектура (как задумано)
- [specs/](../specs/) — детальные спеки по каждому сервису
- Гэп-анализ этого документа — что из спек реально сделано
