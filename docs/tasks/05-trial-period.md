# 05. Автоматический пробный период 3 дня новым пользователям

**Дата:** 2026-04-23
**Статус:** 🟢 Утверждено — все решения приняты, готово к имплементации
**Автор:** Devin + aziz
**Родительский:** [02-mvp-c-implementation.md](./02-mvp-c-implementation.md) — новая фича поверх Этапа 4 (subscription) и Этапа 2 (vpn-service)

---

## 🎯 Цель

Новый пользователь, впервые открывший Mini App через Telegram, **автоматически и без оплаты** получает:
- Подписку со статусом `trial`, длительностью **3 дня**, на **2 устройства**
- Работающий VPN-доступ (UUID прописан во всех активных Xray inbound'ах)
- Понятное уведомление во фронте: «вам активирован пробный период на 3 дня»

Триал даётся **ровно один раз в жизни** каждому Telegram-аккаунту. Повторная регистрация того же `telegram_id` триал не выдаёт.

---

## 📚 Контекст / подтверждение проблемы

Сейчас (после `openclaw agents` — 23 апреля):

```sql
SELECT u.id, u.telegram_id, u.username, s.status, s.expires_at
FROM users u LEFT JOIN subscriptions s ON s.user_id=u.id;

-- id | telegram_id |  username  | status |    expires_at
-- ----+-------------+------------+--------+----------------
--  4 |  6942617295 | azizzv     | (NULL) | (NULL)   ← новый юзер БЕЗ подписки
--  3 |   164015255 | aziztwelve | active | 2026-05-22   ← создан вручную
```

Последний зарегистрированный через Mini App юзер **не получил никакой подписки** — в `subscriptions` нет строки → `vpn_users` нет → Mini App ничего не может показать. Юзер видит «нет подписки, купите» с первой же сессии — отталкивающий UX для MVP коммерческого продукта.

---

## 🏗 Архитектура

```
                   Telegram Mini App
                          │
                  POST /api/v1/auth/validate
                          │
                          ▼
 ┌─────────────────────────────────────────────────────────┐
 │  Gateway (orchestrator)                                  │
 │                                                          │
 │  1. auth-service.ValidateTelegramUser                    │
 │     → User{id, ...}, JWT, is_new_user: bool              │
 │                                                          │
 │  2. IF is_new_user:                                      │
 │     a. subscription-service.StartTrial(user_id)          │
 │        → Subscription{id, status="trial", expires_at}    │
 │     b. vpn-service.CreateVPNUser(user_id, sub_id)        │
 │        → UUID                                            │
 │                                                          │
 │  3. RETURN { user, token, trial_activated: true/false,   │
 │              subscription?: {...} }                      │
 └─────────────────────────────────────────────────────────┘
```

**Где оркестрация:** Gateway, не auth-service. `auth-service` должен знать только про пользователей, без связей с subscription/vpn. Gateway уже имеет клиентов всех трёх сервисов — естественное место для coordination.

---

## 🧩 Изменения

### Stage 1: БД (миграции)

**`services/subscription-service/migrations/003_add_trial_support.up.sql`:**
```sql
-- Flag plan как "триальный" — такие планы НЕ показываются в UI как
-- покупаемые, но используются для StartTrial.
ALTER TABLE subscription_plans ADD COLUMN is_trial BOOLEAN NOT NULL DEFAULT false;

-- Trial-plan (2 устройства, 3 дня, цена 0 — не для продажи)
INSERT INTO subscription_plans (id, name, duration_days, max_devices, base_price, price_stars, is_active, is_trial)
VALUES (99, 'Пробный период', 3, 2, 0.00, 0, true, true);

-- Service-level идемпотентность: помечаем в users что триал был выдан.
-- Позволяет переиспользовать тот же telegram_id (например после delete user)
-- и всё равно не дать второй триал.
ALTER TABLE users ADD COLUMN trial_used_at TIMESTAMPTZ;
```

**`003_add_trial_support.down.sql`:**
```sql
ALTER TABLE users DROP COLUMN IF EXISTS trial_used_at;
DELETE FROM subscription_plans WHERE id = 99;
ALTER TABLE subscription_plans DROP COLUMN IF EXISTS is_trial;
```

**Открытый вопрос #1:** колонка `users.trial_used_at` живёт в таблице `users` (которую owner'ит auth-service), а миграция лежит в `subscription-service/migrations/`. Либо переносим ALTER в auth-service, либо (предпочтительнее) у нас cross-service миграция — тогда надо чётко указать порядок в `migrate:up`-таске. Второй вариант проще и уже сложился де-факто (все миграции идут последовательно).

### Stage 2: `subscription-service` — новая RPC `StartTrial`

**Proto** (`shared/proto/subscription/v1/subscription.proto`):
```proto
rpc StartTrial(StartTrialRequest) returns (StartTrialResponse);

message StartTrialRequest {
  int64 user_id = 1;
}

message StartTrialResponse {
  Subscription subscription = 1;
  bool was_already_used = 2;  // true → trial_used_at уже стоит, подписка не создана
}
```

**Service** (`internal/service/subscription.go`):
```go
func (s *SubscriptionService) StartTrial(ctx context.Context, userID int64) (*model.Subscription, bool, error) {
    // Атомарно: check trial_used_at + INSERT subscription + UPDATE trial_used_at
    // Использует transaction'ный метод в repo.
    sub, alreadyUsed, err := s.repo.StartTrialTx(ctx, userID)
    if err != nil {
        return nil, false, err
    }
    return sub, alreadyUsed, nil
}
```

**Repository** (`internal/repository/subscription.go`):
- `StartTrialTx` — одна транзакция:
  1. `SELECT trial_used_at FROM users WHERE id=$1 FOR UPDATE;` — lock-аем юзера
  2. Если `trial_used_at IS NOT NULL` → вернуть `already_used=true`
  3. `SELECT id, duration_days, max_devices FROM subscription_plans WHERE is_trial=true AND is_active=true LIMIT 1;`
  4. `INSERT INTO subscriptions (user_id, plan_id, max_devices, total_price, started_at, expires_at, status) VALUES ($1, trial_plan.id, trial_plan.max_devices, 0, NOW(), NOW()+interval '3 days', 'trial') RETURNING *;`
  5. `UPDATE users SET trial_used_at = NOW() WHERE id = $1;`
  6. COMMIT

### Stage 3: `auth-service` — флаг `is_new_user`

**Proto** (`shared/proto/auth/v1/auth.proto`):
```proto
message ValidateTelegramUserResponse {
  User user = 1;
  string jwt_token = 2;
  bool is_new_user = 3;  // ← новый
}
```

**Service** (`internal/service/auth.go`):
- Возвращать `(user, token, isNew, error)` где `isNew=true` если `userRepo.GetUserByTelegramID` вернул `not found` и был создан новый user

### Stage 4: `vpn-service.CreateVPNUser` — уже есть

`CreateVPNUser(user_id, subscription_id)` уже реализован, best-effort multi-server. Переиспользуем без изменений.

### Stage 5: Gateway — оркестрация

**Handler** (`internal/handler/auth.go` → `ValidateTelegramUser`):
```go
// 1. Валидация и создание/апдейт юзера
resp, err := h.authClient.ValidateTelegramUser(ctx, initData)

// 2. Новый юзер → активируем триал + VPN
var trialSub *pb.Subscription
trialActivated := false
if resp.IsNewUser {
    subResp, err := h.subClient.StartTrial(ctx, resp.User.Id)
    if err != nil {
        log.Error("trial creation failed", ...)
        // Не валим весь auth — юзер сможет купить подписку вручную
    } else if !subResp.WasAlreadyUsed {
        trialSub = subResp.Subscription
        trialActivated = true

        _, err := h.vpnClient.CreateVPNUser(ctx, resp.User.Id, trialSub.Id)
        if err != nil {
            log.Error("vpn user creation failed after trial", ...)
            // Триал есть, VPN нет → фронт должен показать "свяжитесь с саппортом".
            // Background cron-ретрай можно добавить позже.
        }
    }
}

// 3. Отдаём клиенту
json.NewEncoder(w).Encode(AuthResponse{
    User:            resp.User,
    Token:           resp.JwtToken,
    TrialActivated:  trialActivated,
    Subscription:    trialSub,
})
```

### Stage 6: Frontend (`vpn_next`)

1. В страницу `/` (или `/connect`) — после `/auth/validate` если `trial_activated === true` → показать onboarding-бэннер «🎁 Вам активирован пробный период на 3 дня. Подключайтесь!»
2. В `/account` — показывать отсчёт до `expires_at`, если статус `trial`. Кнопка «Продлить» → `/plans`
3. За 24ч до истечения триала — можно добавить push через Telegram бот (отдельная задача)

---

## ✅ Definition of Done

- [ ] Миграция `003_add_trial_support.up.sql` написана, `down.sql` корректно откатывает
- [ ] Триал-план (id=99, name="Пробный период", 3 дня, 2 устройства, is_trial=true) создаётся
- [ ] `subscription-service.StartTrial` RPC: транзакция (SELECT…FOR UPDATE + INSERT + UPDATE), повторный вызов возвращает `was_already_used=true` без создания дубликата
- [ ] `auth-service.ValidateTelegramUser` возвращает `is_new_user`
- [ ] Gateway `/api/v1/auth/validate`: при `is_new_user=true` вызывает StartTrial + CreateVPNUser, отдаёт `trial_activated` + `subscription` в ответе
- [ ] Если какая-то из этих ступенек падает — JWT всё равно отдаётся (не блокируем логин)
- [ ] Frontend (vpn_next): баннер «Пробный период 3 дня» при первом заходе, отсчёт в account-странице
- [ ] E2E smoke: новый Telegram-юзер открывает Mini App → внутри 10 секунд видит баннер + работающий VPN link
- [ ] Старый юзер (`trial_used_at IS NOT NULL`) — повторно триал не получает
- [ ] `expire_cron` (уже есть) корректно переводит истёкший триал в `expired`
- [ ] Документация: обновить `02-mvp-c-implementation.md` Этап 4 — ссылка на эту задачу

---

## ⚠️ Риски и ограничения

| Риск | Митигация |
|---|---|
| Злоупотребление: один человек создаёт N Telegram-аккаунтов → N триалов | Triality лимитится по `telegram_id` (уникально). Борьба с мульти-аккаунтами — отдельная задача (IP-rate-limit, device fingerprint, банки Telegram SIM — это fraud-prevention, вне MVP) |
| Race между двумя параллельными `/auth/validate` того же нового юзера | `SELECT ... FOR UPDATE` в `StartTrialTx` + UNIQUE-индекс `(user_id, status='trial')` защищают от дублей |
| VPN creation упал, триал уже в БД | Фоновый cron-retry `vpn.CreateVPNUser` для subscription'ов без vpn_user'а. MVP: отображать "проблема с VPN, обновите страницу через минуту" |
| Юзер покупает подписку поверх активного триала | Суммируем остаток дней: `GREATEST(NOW(), existing.expires_at) + plan.duration_days` (см. решение #3) |
| Backfill для существующих юзеров имеет race с новой регистрацией | Запускаем скрипт с `WHERE trial_used_at IS NULL` и кладём транзакцию — идемпотентно, повторный запуск ничего не ломает |

---

## ✅ Принятые решения

1. **Миграция `users.trial_used_at` — в `auth-service/migrations/`.** Users это auth-домен, schema ownership должен быть чистым. Port order в `migrate:up` (auth → sub → vpn → payment) уже работает, `subscription-service` видит колонку к моменту своих миграций.

2. **Длительность триала — 3 дня.** MVP-значение для высокой конверсии. Легко поменять (одно значение в seed trial-плана).

3. **Покупка подписки поверх активного триала — суммирование дней.**
   ```
   new_expires_at = GREATEST(NOW(), existing.expires_at) + plan.duration_days
   ```
   - Если триал ещё не истёк → оставшиеся дни триала плюсуются к оплаченному периоду
   - Если триал истёк (status='expired') → считается от NOW(), как обычная покупка
   - Одна строка в `subscriptions` на юзера, обновляется in-place: меняется `plan_id`, `status` (trial → active), `max_devices`, `expires_at`, `total_price`
   - **Итог:** юзер, купивший план на 3-й день триала, получит **3 дня остатка + 30 дней** = 33 дня. Воспринимается как бонус, а не штраф за "раннюю покупку".

4. **`max_devices=2` у триала (пересмотрено).** Изначально было 1, но это создавало трение ещё до первой оплаты: у нового юзера почти всегда есть пара устройств (телефон+ноут). Конверсионный драйвер переносим на длительность (3 дня) и количество локаций. Изменение пришло миграцией `004_trial_two_devices.up.sql`.

5. **One-time backfill для существующих юзеров.** SQL-скрипт `deploy/scripts/backfill-trial.sql` — выдаёт триал всем `users.trial_used_at IS NULL`. Выполняется вручную один раз на prod-БД после миграции.

6. **`/auth/validate` НЕ возвращает `vless_link`.** Ответ включает только `{user, token, trial_activated, subscription}`. Линк получается отдельным `GET /api/v1/vpn/servers/{id}/link?device_id=...` когда юзер выбрал сервер в UI. Причины:
    - `GetVLESSLink` имеет побочный эффект — `UPSERT active_connections` (занимает слот устройства). Фейковый `initial` device_id в `/auth/validate` создал бы мёртвый слот и быстро упёр бы юзера в `device_limit` (на триале с `max_devices=2` второй слот тратится за один клик).
    - `device_id` — это localStorage UUID клиента, сервер его не знает до первого явного запроса.
    - Multi-server архитектура: дефолтный сервер в `/auth/validate` навязывал бы скрытую логику (по load? по geo?) в неожиданное место.
    - Семантика чище: auth — "кто ты", vpn — "подключение".
    - Quick-Connect ("1 клик") при необходимости добавим отдельной ручкой `GET /api/v1/vpn/quick-connect`, не меняя auth-flow.

---

## 🔗 Ссылки

- Родительский: [02-mvp-c-implementation.md § 4](./02-mvp-c-implementation.md) — Subscription Service
- Связанное: [04-caddy-auto-tls.md](./04-caddy-auto-tls.md) (инфра уже работает)
- Precedent: [Telegram Stars integration](../services/payment-integration.md) — аналогичная cross-service оркестрация
