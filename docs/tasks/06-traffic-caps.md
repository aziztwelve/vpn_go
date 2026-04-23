# 06. Лимиты трафика на тариф (vs abusers без per-device UUID)

**Дата:** 2026-04-23
**Статус:** 🟡 Черновик — обсуждаем перед имплементацией
**Автор:** Devin + aziz
**Родительский:** [02-mvp-c-implementation.md](./02-mvp-c-implementation.md) — новая фича поверх Этапа 4 (subscription)
**Связано:** [05-trial-period.md](./05-trial-period.md) — триал тоже получит cap (или нет, см. вопросы)

---

## 🎯 Цель

Ввести **лимит трафика на UUID в календарный период** (например, 200 GB/мес на базовом тарифе). Главная задача: **ограничить экономическую привлекательность шаринга/перепродажи** без того чтобы перестраивать архитектуру на per-device UUID.

**Почему не per-device UUID:**

Обсуждено в чате 23 апреля: принципиальная разница — "замок" и "ключи". Сейчас у одного юзера один UUID; он может скопировать VLESS-ссылку на N устройств, Xray их не различает. Полный per-device fix потребует переделки `vpn_users` и `CreateVPNUser` (3-4ч). **Вместо этого** принимаем модель "один ключ — общий бюджет трафика". Если шарят с женой — всё ок (легко вписываются в 200 GB). Если перепродают на 100 человек — съедят лимит за пол-дня, блок автоматом.

---

## 📚 Контекст / бизнес-логика

Почему шаринг не проблема:

1. **Семья (2-4 устр.)** — 50-100 GB/мес, лимит в 200 не трогает → юзер доволен
2. **Перепродажа (20+)** — трафик сгорит через 1-2 дня, UUID блокируется, перепродажник теряет клиентов, завет прекращается
3. **Корп-абьюз (10 коллег)** — через пол-месяца упрутся в cap, начнут покупать индивидуально

**Альтернативы которые отклонены:**

| Что | Почему не делаем |
|---|---|
| Per-device UUID (отдельный ключ на каждое устройство) | 3-4ч разработки, ломает "семейный" UX, переход неочевиден для MVP |
| IP-based rate-limit в Xray | Рубит легитимные сценарии (юзер за NAT с несколькими своими устройствами) |
| ML-детект аномалий | Overkill, high false-positive rate |
| Принудительный logout "старых" устройств | UX-катастрофа (потерял телефон → потерял ноут) |

---

## 🏗 Архитектура

```
  ┌──────────────────────────────────────────────────────────────────┐
  │  vpn-service                                                      │
  │                                                                   │
  │  TrafficCron (тикер 5 мин)                                        │
  │  ─ каждые 5 мин:                                                  │
  │    FOR each active vpn_user:                                      │
  │      xray.GetUserStats(email, reset=true)  ← NEW: reset=TRUE      │
  │      → Uplink + Downlink                                          │
  │      → INSERT INTO traffic_log (vpn_user_id, bytes, collected_at)│
  │                                                                   │
  │  ─ потом:                                                         │
  │    FOR each subscription WITH traffic_cap_gb != NULL:             │
  │      used = SUM(traffic_log.bytes WHERE collected_at              │
  │                 >= subscription.started_at                        │
  │                 AND vpn_user_id IN subscription_vpn_users)        │
  │      IF used > plan.traffic_cap_gb * 1 GiB:                       │
  │        UPDATE subscriptions SET status='traffic_exceeded'         │
  │        xray.RemoveUser(uuid)  ← отрубаем доступ                   │
  │        notify user (Telegram bot → отдельная задача)              │
  │                                                                   │
  │  При renew/upgrade → new period → суммирование обнуляется         │
  └──────────────────────────────────────────────────────────────────┘
```

**Ключевые решения:**
- `reset=true` при `GetUserStats` — каждые 5 мин забираем инкремент и копим его в БД. Это защищает от рестарта Xray (его in-memory счётчик обнуляется), от ресинка UUID, от миграции на 2-й VPS.
- Лимит считается **с момента `started_at` текущей подписки**, не календарный месяц. При renew (смене plan / продлении) — `started_at` обновляется → новый период.
- Истёкшая по трафику подписка получает **новый статус `traffic_exceeded`** (не `expired` — чтобы не путать с срок-expired). При renew юзер снова активен.

---

## 🧩 Изменения

### Stage 1: Миграции

**`services/subscription-service/migrations/004_add_traffic_caps.up.sql`:**
```sql
-- Cap трафика в GB (nullable — NULL означает "безлимит").
ALTER TABLE subscription_plans
    ADD COLUMN IF NOT EXISTS traffic_cap_gb INTEGER;

-- Обновляем существующие планы. Триал — без cap'а (3 дня всё равно).
UPDATE subscription_plans SET traffic_cap_gb = 200  WHERE id = 1;   -- 1 мес
UPDATE subscription_plans SET traffic_cap_gb = 500  WHERE id = 2;   -- 3 мес
UPDATE subscription_plans SET traffic_cap_gb = 700  WHERE id = 3;   -- 6 мес
UPDATE subscription_plans SET traffic_cap_gb = 1000 WHERE id = 4;   -- 12 мес
-- id=99 (trial) — оставляем NULL (безлимитный короткий триал)

-- Для подписок. CHECK-constraint status — теперь добавляем 'traffic_exceeded'.
-- (Сейчас constraint'а нет, но если появится — не забыть.)
```

**`services/vpn-service/migrations/004_add_traffic_log.up.sql`:**
```sql
-- Инкрементальный лог. Не суммарные счётчики (потеряем при рестарте Xray),
-- а именно дельты. Суммирование при запросе.
CREATE TABLE IF NOT EXISTS traffic_log (
    id BIGSERIAL PRIMARY KEY,
    vpn_user_id BIGINT NOT NULL REFERENCES vpn_users(id) ON DELETE CASCADE,
    uplink_bytes BIGINT NOT NULL DEFAULT 0,
    downlink_bytes BIGINT NOT NULL DEFAULT 0,
    collected_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_traffic_log_vpn_user_id_time
    ON traffic_log (vpn_user_id, collected_at DESC);

-- partial-index для быстрого "трафик с момента started_at"
-- (sub-query будет WHERE collected_at >= $1 AND vpn_user_id IN (...))
```

**Ротация:** старые строки `traffic_log.collected_at < NOW() - INTERVAL '90 days'` удаляет отдельный cleanup-cron (раз в сутки). Не пухнет.

### Stage 2: Proto

**`shared/proto/subscription/v1/subscription.proto`:**
```proto
message SubscriptionPlan {
  ...
  int32 traffic_cap_gb = 9;  // 0 → безлимит
}

message Subscription {
  ...
  int64 traffic_used_bytes = 11;      // суммарно использовано
  int64 traffic_cap_bytes = 12;       // 0 → безлимит
  int32 traffic_percent_used = 13;    // 0-100, для UI-прогрессбара
}
```

**`shared/proto/vpn/v1/vpn.proto`:**
```proto
// Дёргается subscription-service'ом при GetActiveSubscription
rpc GetUserTrafficUsage(GetUserTrafficUsageRequest) returns (GetUserTrafficUsageResponse);

message GetUserTrafficUsageRequest {
  int64 user_id = 1;
  string since = 2;  // ISO8601 — от started_at подписки
}

message GetUserTrafficUsageResponse {
  int64 uplink_bytes = 1;
  int64 downlink_bytes = 2;
  int64 total_bytes = 3;
}
```

### Stage 3: vpn-service — TrafficCron

**`services/vpn-service/internal/service/traffic_cron.go`** (новый):
```go
type TrafficCron struct {
    repo   *repository.VPNRepository
    xray   *xray.Client
    subCli subscriptionpb.SubscriptionServiceClient
    logger *zap.Logger
}

func (c *TrafficCron) Run(ctx context.Context) {
    tick := time.NewTicker(5 * time.Minute)
    defer tick.Stop()
    for {
        select {
        case <-ctx.Done(): return
        case <-tick.C:
            c.collectTraffic(ctx)
            c.enforceCapsAndBlock(ctx)
        }
    }
}

func (c *TrafficCron) collectTraffic(ctx context.Context) {
    users, _ := c.repo.ListAllVPNUsers(ctx)
    for _, u := range users {
        // reset=true → забираем инкремент и обнуляем в Xray.
        // Если Xray рестартнулся — счётчик уже 0, возвращаем 0, OK.
        stats, err := c.xray.GetUserStats(ctx, u.Email, true)
        if err != nil { continue }
        if stats.Uplink == 0 && stats.Downlink == 0 { continue }
        _ = c.repo.InsertTrafficLog(ctx, u.ID, stats.Uplink, stats.Downlink)
    }
}

func (c *TrafficCron) enforceCapsAndBlock(ctx context.Context) {
    // Через gRPC sub-service получаем список active с cap'ом.
    // Для каждого — SUM traffic_log по user_id + compare с cap.
    // При превышении → status=traffic_exceeded + xray.RemoveUser.
    // Реализация подробно в репо + service слое sub-service.
}
```

**`services/vpn-service/internal/repository/traffic.go`** (новый):
```go
func (r *VPNRepository) InsertTrafficLog(ctx context.Context, vpnUserID int64, ul, dl int64) error
func (r *VPNRepository) SumTrafficSince(ctx context.Context, vpnUserID int64, since time.Time) (int64, error)
```

**`services/vpn-service/internal/app/app.go`:**
```go
// +TrafficCron в Start(), рядом с LoadCron и resyncOnStartup
go a.trafficCron.Run(trafficCtx)
```

### Stage 4: subscription-service — enforcement + reporting

**`services/subscription-service/internal/service/traffic_enforcement.go`** (новый):
- `CheckAndBlockExceeded(ctx)` — SELECT все подписки с cap'ом → для каждой query vpn-service `GetUserTrafficUsage(since=sub.started_at)` → compare → UPDATE status.
- Вызывается из TrafficCron (межсервисный вызов) ИЛИ из собственного cron'а в sub-service.

**Предпочтительно — enforce в sub-service**, т.к. sub-service владеет subscriptions-таблицей.

**`internal/service/subscription.go` → `GetActiveSubscription()`:**
- Теперь возвращает `traffic_used_bytes`, `traffic_cap_bytes`, `traffic_percent_used` для UI.
- Вычисляет sum через gRPC vpn-service (с кэшированием на 1 мин).

### Stage 5: Gateway — expose в API

- `/api/v1/subscriptions/active` в JSON теперь возвращает `traffic_used_bytes`, `traffic_cap_bytes`, `traffic_percent_used`.
- Новый хендлер `DELETE /api/v1/admin/subscriptions/{id}/reset-traffic` — админ-ручка обнуления (для техподдержки).

### Stage 6: Frontend (vpn_next)

- На `/account` (или home-card) — прогрессбар `42 GB / 200 GB (21%)`. При 80% — жёлтый, при 95% — красный + кнопка "Продлить тариф".
- Если `status === 'traffic_exceeded'` — страница `/traffic-limit` с CTA upgrade.
- Баннер в main после 90% usage — "Кончается трафик".

### Stage 7: Cleanup cron (dev-ops)

Раз в сутки — `DELETE FROM traffic_log WHERE collected_at < NOW() - INTERVAL '90 days'`. Простой cron в `vpn-service` или отдельный psql-скрипт.

---

## ✅ Definition of Done

- [ ] `subscription_plans.traffic_cap_gb` — добавлено, seed-значения для всех 4 планов; триал NULL
- [ ] `traffic_log` таблица в `vpn-service` mig 004, индекс на (vpn_user_id, collected_at)
- [ ] `TrafficCron` собирает инкременты каждые 5 мин, `reset=true`, не дублирует
- [ ] Enforcement-логика: при превышении → `status='traffic_exceeded'` + `xray.RemoveUser` + VPN-коннекты рвутся в течение 5 мин
- [ ] `GetActiveSubscription` gRPC возвращает traffic-поля
- [ ] Gateway `/api/v1/subscriptions/active` передаёт их в JSON
- [ ] Frontend: прогрессбар + warning-баннер при 80%/95%
- [ ] Renew / upgrade → `started_at` обновляется → трафик считается с нуля (нет багов наследования)
- [ ] Cleanup-cron `traffic_log` > 90 дней — работает
- [ ] Документация: Admin-интерфейс (пока SQL) для ресета трафика при жалобах в саппорт
- [ ] Smoke-test: подписка на 1 GB, fake-load 1.5 GB через VPN → cron должен заблокировать через ≤10 мин

---

## ⚠️ Риски и ограничения

| Риск | Митигация |
|---|---|
| Xray-рестарт теряет in-memory счётчик между тиками cron'а | `reset=true` раз в 5 мин делает intervals коротким; потеряем max 5-мин трафика (≈50 MB на активного юзера — пренебрежимо) |
| Cron не успевает за 5 мин при N > 10k юзеров | Batch-запросы к Xray (pattern `user>>>*>>>traffic`) + batch-insert в traffic_log. До 10k юзеров — не проблема. |
| Юзер превысил cap, но Xray уже дал ему фиктивно "бесплатный" трафик между тиками | До 5 мин "льготного" превышения. Приемлемо для MVP. В проде можно снизить интервал до 1 мин. |
| Ресет трафика через SQL-редактирование `started_at` — не красиво | Отдельная админ-ручка позже + логирование операций |
| Бан юзера → `xray.RemoveUser` → его клиенты отваливаются мгновенно, UX-шок | Пуш в Telegram бот "Трафик исчерпан, продли тариф" за день до и в момент бана |
| Обнуление трафика при upgrade — bug-prone | Тест: подписка trial (cap=NULL) → купить 1 мес (cap=200 GB) → `started_at=NOW()` → sum = 0 GB |
| Reset=true race: если два процесса одновременно делают GetUserStats | Только один `TrafficCron` инстанс в одном vpn-service — гарантия через unique-констрейнт + mutex'а не нужно |

---

## 🤔 Открытые вопросы (обсудить до имплементации)

1. **Дефолтные cap'ы** — какие цифры?
    - Мой голос: `200 / 500 / 700 / 1000 GB`. Можно скорректировать после первой недели прод-метрик.
    - Альтернатива: `100 / 300 / 500 / 800` — строже, больше шансов на upgrade.
    - Альтернатива: `NULL` на всех изначально → включим cap позже, когда появятся abuser'ы.

2. **Триал — cap или нет?**
    - 3 дня × 24ч × 100 Mbit = теоретически 3.2 TB, но практически юзер съест 30-50 GB
    - Моё мнение: **NULL для триала**. 3 дня и так короткие, если юзер закачал 100 GB за триал — он хороший кандидат на платную подписку.
    - Альтернатива: 50 GB cap — для защиты от trial-фарминга.

3. **Что делать при превышении?** (жестокость)
    - A. `RemoveUser` → мгновенный disconnect + status=traffic_exceeded. Юзер не может пользоваться пока не продлит.
    - B. Throttle до 1 Mbit/s (через Xray `limitFallback`) → медленно, но работает. Меньше UX-шок.
    - C. Warning-only до следующего цикла, disconnect через сутки grace period.
    - Мой голос: **A для MVP**. Жёстко, понятно, подталкивает к конверсии. B — через месяц, когда будет понятна модель.

4. **Период учёта — от `started_at` или calendar month?**
    - A. `started_at` — купил 12 марта → период до 12 апреля → потом 12 апреля → май 12.
    - B. Calendar — 1-е марта - 31 марта, 1-е апр - 30 апр.
    - Мой голос: **A**. Синхронно с `expires_at`, нет сложностей с proration.

5. **Admin-ручка ресета трафика?** (для саппорта)
    - Нужна, но можно на MVP через psql-команду, потом вынести в admin-service.
    - Пока SQL — `UPDATE subscriptions SET started_at = NOW() WHERE id = $1` (и пересчитать sum в запросе).

6. **Уведомления юзеру** (80%, 95%, исчерпан)?
    - Telegram bot sendMessage через `platform/pkg/telegram/client.go` (уже есть).
    - **Входит в этот таск или вынесем в 07?**
    - Мой голос: **вынесем**. Функционально не блокирует cap-enforcement.

7. **Счёт трафика для non-active серверов** (добавили 2-й VPS, юзер юзает оба)?
    - `GetUserStats` должен сумить по всем серверам где у юзера UUID.
    - Для multi-server: `TrafficCron` в vpn-service ходит в каждый `xray_api_host` из vpn_servers.
    - Сейчас у нас 1 сервер → неактуально, но нужно правильно смоделировать repo.

8. **Concurrent sessions как второй cap?** (например, max 3 одновременных подключения)
    - Требует integration с Xray observability (какие IP онлайн)
    - Сложнее traffic-cap'а, вынесем в задачу 07 отдельно.

9. **Cap per-device vs per-user?** 
    - Здесь всегда per-user (один UUID). Per-device — в задаче 06.5 если когда-нибудь сделаем per-device UUID.

---

## 🔗 Ссылки

- Родительский: [02-mvp-c-implementation.md § 4](./02-mvp-c-implementation.md) — Subscription Service
- Связанное: [05-trial-period.md](./05-trial-period.md) — формат задач и precedent
- Xray Stats API: `platform/pkg/xray/client.go::GetUserStats` (уже реализовано)
- Precedent: схожая модель у Netflix (до 2023), LinkedIn Premium, AWS Free Tier
