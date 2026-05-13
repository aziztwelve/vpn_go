# 19. Per-campaign trial override

**Дата:** 2026-05-08
**Статус:** ✅ Задеплоено + e2e smoke-test пройден (30 дней через `src_varzish`)
**Автор:** aziz + Devin
**Связано:** [`research/competitor-extravpn.md`](../research/competitor-extravpn.md), task 18, миграция [`subscription-service/010_repricing_and_trial_3d.up.sql`](../../services/subscription-service/migrations/010_repricing_and_trial_3d.up.sql)

---

## 🎯 Цель

Дать админу возможность **создавать рекламные ссылки с увеличенной длительностью триала** (например, 7/15/30/60/90 дней) — не трогая default-flow `/start` и юзерскую реф-программу. Бизнес-кейс: заплатить блогеру за охват, дать его аудитории «жирный» триал → конверт в платных выше, чем со стандартных 3 дней.

## 📚 Контекст

В `campaigns` (миграция `referral-service/003_create_campaigns.up.sql`, 24 апреля) уже всё готово для атрибуции:

```
/start src_<slug>
  → bot_starts.campaign_id        (метрика воронки)
  → pending_campaigns              (если юзер ещё не зарегистрирован)
  → user_attribution               (first-touch, неизменна)
```

Сейчас триал — **жёстко 3 дня** через `subscription_plans.id=99 (duration_days=3)`. См. `subscription-service/internal/repository/subscription.go:265` (`StartTrialTx`):

```sql
INSERT INTO subscriptions (... expires_at, ...)
VALUES (... NOW() + INTERVAL '1 day' * trialPlan.DurationDays, ...)
```

Юзерские реф-ссылки (`referral_links`) — НЕ трогаем (Aziz: «не для ссылки для рефералки у юзеров, а то, что я сам создаю для рекламы»).

## 🏗 Решение

### Архитектура

`StartTrial` вызывается **после** `RegisterFromBot`/`ValidateTelegramUser`, когда `user_attribution` уже зафиксирована. Значит **subscription-service может сам резолвить override через JOIN**, и callsite'ам в gateway/auth не нужно ничего пробрасывать.

```sql
INSERT INTO subscriptions (... expires_at, ...)
VALUES (
    ...,
    NOW() + INTERVAL '1 day' * COALESCE(
        (SELECT c.trial_duration_days
         FROM user_attribution ua
         JOIN campaigns c ON c.id = ua.campaign_id
         WHERE ua.user_id = $user_id AND c.archived_at IS NULL),
        $plan_default_days   -- 3 дня (из subscription_plans.id=99)
    ),
    ...
)
```

Семантика:
- Юзер пришёл по `src_<slug>` с `trial_duration_days=30` → триал на 30 дней.
- Юзер пришёл по `src_<slug>` с `trial_duration_days IS NULL` → дефолт 3 дня.
- Юзер пришёл без `src_` (обычный `/start` / `ref_<token>`) → user_attribution пуст → дефолт 3 дня.
- Кампания заархивирована к моменту `StartTrial` → дефолт 3 дня (защита от ретро-замены).

### Допустимые значения

**Пресеты: 3, 7, 15, 30, 60, 90.** Остальное на уровне БД отклоняем CHECK'ом, на уровне сервиса — явной валидацией. Если когда-то понадобится 14 дней — добавим в список одной строкой кода + миграцией CHECK'а.

NULL = «дефолт 3 дня» (= не задано). Явный `0` — отклоняем как невалидное значение.

### Что НЕ меняется

- `RegisterFromBot`, `ValidateTelegramUser`, `activateTrialFromBot`, `activateTrial`
- `referral_links`
- `bot_starts.campaign_id` / `pending_campaigns` / `user_attribution`
- Существующие `subscriptions` (у активных триалов `expires_at` уже зафиксирован)

## 📁 Изменения по файлам

### Backend

1. **`services/referral-service/migrations/004_campaign_trial_override.up.sql`** (NEW):
   ```sql
   ALTER TABLE campaigns ADD COLUMN IF NOT EXISTS trial_duration_days INT
       CHECK (trial_duration_days IS NULL OR trial_duration_days IN (3,7,15,30,60,90));
   COMMENT ON COLUMN campaigns.trial_duration_days IS
       'Override длительности триала для юзеров, пришедших по src_<slug>. NULL = дефолт subscription_plans.id=99';
   ```
   `+down.sql` снимает колонку.

2. **`shared/proto/campaign/v1/campaign.proto`**:
   - `Campaign.trial_duration_days = 12` (`int32`, 0 = NULL)
   - `CreateCampaignRequest.trial_duration_days = 7`
   - `UpdateCampaignRequest.trial_duration_days = 6` с sentinel `-1` = «обнулить» (как `partner_user_id` / `payout_percent`)

3. **`services/referral-service/internal/model/campaign.go`**:
   - `Campaign` поле `TrialDurationDays *int32`

4. **`services/referral-service/internal/repository/campaign.go`**:
   - SELECT/INSERT/UPDATE включают `trial_duration_days`
   - Update: `null` если `*int32 == nil`, `NULL` если «обнулить» через sentinel

5. **`services/referral-service/internal/service/campaign.go`**:
   - Валидация: `nil` ИЛИ значение из `[3,7,15,30,60,90]`. Иначе `InvalidArgument`.

6. **`services/referral-service/internal/api/campaign.go`**:
   - Маппинг proto↔model (sentinel `-1` → `*int32 = nil` (обнулить); `0` → не менять; остальное → `*int32`)

7. **`services/subscription-service/internal/repository/subscription.go` (`StartTrialTx`)**:
   - SQL `INSERT` использует `COALESCE((SELECT c.trial_duration_days FROM user_attribution ua JOIN campaigns c ON c.id=ua.campaign_id WHERE ua.user_id=$1 AND c.archived_at IS NULL), $4)` вместо чистого `$4`.

8. **`services/gateway/internal/client/campaign.go`**:
   - `CreateCampaign` / `UpdateCampaign` принимают `trialDurationDays *int32`

9. **`services/gateway/internal/handler/admin_campaigns.go`**:
   - JSON структуры: `TrialDurationDays *int32 \`json:"trial_duration_days"\``
   - `campaignToJSON` включает поле

### Frontend (vpn_next)

10. **`lib/campaign.ts`**: `Campaign.trial_duration_days?: number | null`
11. **`app/admin/campaigns/new/page.tsx`**: select-пресеты `[Default 3, 7, 15, 30, 60, 90]`
12. **`app/admin/campaigns/[id]/page.tsx`**: тот же select в форме редактирования + отображение в карточке
13. **`app/admin/campaigns/page.tsx`**: колонка «Триал» в списке (опционально, если влезает)

## 🧪 Smoke-test

После деплоя:
1. Создать через UI кампанию `test_30d` с `trial_duration_days=30`.
2. Получить `deep_link` (`https://t.me/maydavpnbot?start=src_test_30d`).
3. С чужого Telegram-аккаунта (без подписки в системе) кликнуть, пройти `/start` → получить ключ.
4. SQL: `SELECT expires_at - started_at FROM subscriptions WHERE user_id=<new_uid>;` → ожидаем `30 days`.
5. Контроль: создать обычный `/start` от ещё одного нового аккаунта → ожидаем `3 days`.

## 📝 Ограничения / TODO

- Существующих кампаний override = NULL (3 дня) — никаких side-effects.
- Если кампания заархивирована **между** регистрацией и активацией триала (микросекундный рейс через AccountValidate) — fallback на дефолт. Считаем приемлемым.
- Нет проверки «только админ может set 30/60/90» — у нас уже все CRUD под `RequireAdmin`. Любой админ = любой пресет.
- Не пишем `SetActiveDays` history. Если понадобится аудит — на отдельную миграцию.

---

## 🩹 Follow-up: динамический welcome-текст в `/start`

**Когда:** 2026-05-08, после первичного деплоя task 19.

**Проблема:** SQL-override отдаёт юзеру 30 дней триала, но welcome-сообщение бота продолжало писать **«✨ Вы получили 3 дня пробного периода»** — текст был хардкоднут в `sendStartMessage` в `services/gateway/internal/handler/telegram_bot.go`. Юзер с 30-дневной кампании получал когнитивный диссонанс между ботом и Mini App.

**Решение:**
1. `activateTrialFromBot(ctx, telegramID, userID) int` — теперь возвращает фактическую длительность созданного триала. Внутри парсим `Subscription.StartedAt` / `Subscription.ExpiresAt` (RFC3339) → разница в днях с round-up по часам через новую утилиту `trialDaysFromSubscription(*pb.Subscription) int`. `0` = триал не создан (existing user / `WasAlreadyUsed` / nil-Subscription / error / sub-client не настроен).
2. `handleStart` прокидывает это значение в `sendStartMessage`. Для existing-юзеров и при ошибках `RegisterFromBot` — передаём `0`.
3. `sendStartMessage(ctx, chatID, userID, trialDays int)` строит текст через `strings.Builder`. Блок «✨ Вы получили <N> пробного периода» рендерится только если `trialDays > 0`, через уже существующий `pluralizeDays(n)` (1 день / 3 дня / 30 дней — RU-плюрализация по `mod100`/`mod10`).

**Файл:** `services/gateway/internal/handler/telegram_bot.go` — `activateTrialFromBot` (теперь возвращает `int`), новая `trialDaysFromSubscription`, обновлённый `sendStartMessage(... trialDays int)`, оба callsite'а в `handleStart`.

**E2E smoke-test (после деплоя):**

@maydavpn_support (telegram_id=8671603698) — удалили из БД (DELETE FROM users CASCADE → subscriptions / vpn_users / referral_links + ручное DELETE из bot_starts по telegram_id, т.к. там FK на campaigns, а не на users) → `/start src_varzish` → проверка БД:

| Что | Значение |
|---|---|
| `bot_starts.start_param` | `src_varzish` ✅ |
| `bot_starts.campaign_id` | 7 ✅ |
| `user_attribution.campaign_id` | 7 ✅ |
| `subscriptions.status` | `trial` ✅ |
| `subscriptions(expires_at - started_at)` | **30 days** ✅ |
| `vpn_users` | UUID + token созданы ✅ |
| Лог gateway `trial activated from /start` | `trial_days=30` ✅ |

Всё end-to-end отрабатывает: SQL-override → длительность подписки → welcome-сообщение → реальный VLESS-токен у юзера.
