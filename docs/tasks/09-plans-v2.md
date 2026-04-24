# 09. `/plans.v2` — редизайн страницы выбора тарифа + бизнес-фичи

**Дата:** 2026-04-24
**Статус:** 🔵 В работе — витрина тарифов v2 доступна на `/plans/v2`; бизнес-фичи (промо, автопродление) отложены — см. раздел [TODO](#-todo-\u0431\u0438\u0437\u043d\u0435\u0441-\u0444\u0438\u0447\u0438-\u043e\u0442\u043b\u043e\u0436\u0435\u043d\u044b)
**Автор:** Devin + aziz
**Родительский:** [02-mvp-c-implementation.md](./02-mvp-c-implementation.md) — монетизация MVP.
**Связанные:** [05-trial-period.md](./05-trial-period.md) (триал уже есть на бэке), [docs/services/wata-integration.md](../services/wata-integration.md), [docs/services/yoomoney-integration.md](../services/yoomoney-integration.md).

---

## 🎯 Цель

Заменить «голую» форму выбора плана на полноценную **конверсионную витрину тарифов** уровня коммерческих VPN (Windscribe, Mullvad, NordVPN) — с упором на:

1. **Понятность цены** — нет двух разных цифр в одной карточке, «₽/мес» вместо абстрактных «за период», явная экономия на длинных тарифах.
2. **Social proof & якорь** — «Популярный» бейдж, зачёркнутая «старая» цена, «экономия 40%».
3. **Меньше барьеров** — промокоды, автопродление, trial-баннер, «оплатить за 10 сек».
4. **Robustness** — аккуратный payment flow для карточных провайдеров (WATA/YooMoney), не бросать юзера «в пустоту» после `openLink`.

Текущий `/plans` остаётся как fallback и для канареечного сравнения; `/plans/v2` выкатываем на новых пользователей.

---

## 📍 Где живёт

- **Фронт:** `vpn_next/app/plans/v2/page.tsx` (+ компоненты в `vpn_next/components/plans/*`)
- **Утилиты:** `vpn_next/lib/format.ts` (pluralize/formatRub/formatDuration/computeSavings — доля в v1 уже есть локально)
- **API-клиент:** `vpn_next/lib/api.ts` — расширяем типами и методами (`validatePromoCode`, `getPaymentStatus`, `PaymentProvider`).
- **Бэкенд:** никаких несовместимых изменений в текущей итерации. Новые бэкенд-ручки — маркером `TODO(backend)` в коде и в этом документе (см. [Бэкенд-работы](#бэкенд-работы)).

---

## 📚 Контекст / проблема

Текущая страница `/plans` ([vpn_next/app/plans/page.tsx](../../../vpn_next/app/plans/page.tsx)):

```
┌────────────────── План 30 дней ──────────────────┐
│ Месяц                           499 ₽ за период  │  ← base_price за 1 устройство
│ ✓ Базово до 1 устройства                         │
│ ✓ Безлимитный трафик                             │
│ ✓ Локации: USA, DE, SG, JP                       │
└──────────────────────────────────────────────────┘
  Количество устройств: [1 | 2 | 3]
                          499 │ 799 │ 1099 ₽       ← отдельная цена
  Способ оплаты: [⭐ Stars] [💳 WATA] [🏦 YooMoney]
  [ Оплатить 799 ₽ ]                               ← третья цена
```

Проблемы:

| # | Проблема | Эффект |
|---|---|---|
| 1 | 3 разные цены в одном экране | пользователь не понимает, сколько реально заплатит |
| 2 | Нет выделения «выгодного» плана | LTV низкий, все берут минималку на месяц |
| 3 | Stars — дефолт, даже если Mini App открыт вне Telegram | первый же клик — алерт «только в Telegram» |
| 4 | После `openLink` (WATA/YooMoney) — юзер возвращается в Mini App и видит ту же /plans | ощущение, что «ничего не произошло», даблит попытки оплаты |
| 5 | Нет промокода | никаких кампаний, нельзя «в канале анонс + промо NEWYEAR» |
| 6 | Нет автопродления | плохой retention, юзер раз в месяц руками оплачивает или уходит |
| 7 | `full-screen loader` на все плашки | ощущается «тормозит», особенно на слабых мобилках |
| 8 | Emoji 💳 🏦 ⭐ мешаются с lucide-иконками остального UI | не единый стиль |

---

## 🎨 Новый макет `/plans/v2`

```
┌──────────── ⬅ Назад ─────────────── Выбор тарифа ─────────────┐
│  [🎁 Активен триал до 27 апр]   ← если status=trial            │
│                                                                 │
│  ┌─ План: 3 месяца ─┐ ┌─ План: 6 месяцев ─┐ ┌─ План: 1 год ─┐  │
│  │                  │ │  ⭐ ПОПУЛЯРНО     │ │  −40%         │  │
│  │   499 ₽/мес      │ │   399 ₽/мес      │ │   299 ₽/мес   │  │
│  │                  │ │                  │ │               │  │
│  │  за 3 мес        │ │  за 6 мес        │ │  за 12 мес    │  │
│  │  1 497 ₽         │ │  ~~2 994 ₽~~     │ │  ~~5 988 ₽~~  │  │
│  │                  │ │  2 394 ₽         │ │  3 588 ₽      │  │
│  │  ✓ до 2 устр-в   │ │  экономия 600 ₽  │ │  экономия 2400│  │
│  └──────────────────┘ └──────────────────┘ └───────────────┘   │
│                                                                 │
│  Устройств: [ 1  399 ₽/мес ] [ 2  599 ₽/мес ] [ 3  799 ₽/мес ]  │
│                                                                 │
│  🎟  Есть промокод?   [ FRIEND10     ] [ ✓ Применить ]          │
│      Промокод FRIEND10: −10%                                    │
│                                                                 │
│  Способ оплаты:                                                 │
│  ○ ⭐ Telegram Stars  (disabled вне Telegram)                   │
│  ○ 💳 Карта / СБП     — через WATA (рекомендуем)                │
│  ○ 🏦 YooMoney        — карта, кошелёк                          │
│                                                                 │
│  [✓] Продлевать автоматически     можно отменить в любой момент │
│                                                                 │
│  ─────────────────────────────────────────────────────────      │
│  Итого к оплате:                              2 154,60 ₽        │
│  (2 394 − 10% FRIEND10)                                         │
│                                                                 │
│  [            Оплатить 2 154,60 ₽            ]                  │
└────────────────────────────────────────────────────────────────┘
```

### Ключевые отличия от v1

- **Одна цена за период + цена/мес** — в карточке тарифа.
- **Бейдж `Популярно`** на среднем плане (по умолчанию 6 мес), **бейдж `−N%`** на самом длинном.
- **Зачёркнутая полная стоимость** рядом с фактической (якорь).
- **Ряд устройств внутри выбранного плана** — цена/мес пересчитывается в кнопке.
- **Промокод** — inline-поле, client-side демо (до подключения бэка) + API-стаб.
- **Автопродление** — чекбокс с подсветкой «экономия 10% при автопродлении» (опц.)
- **Смарт-дефолт провайдера** — Stars, только если `webApp.openInvoice` реально доступен; иначе первый из `[wata, yoomoney]`.
- **После `openLink` для WATA/YooMoney** — фронт переходит на `/payment/pending?payment_id=...`, который поллит `getPaymentStatus` и сам уводит на `/` при paid.
- **Skeletons** вместо full-screen лоадера.

---

## 🧩 Бизнес-фичи

### 1. Trial-бэйдж на /plans — ✅ сделано

Если `getActiveSubscription().subscription.status === 'trial'` — наверху страницы баннер:

```
🎁 У вас активен триал до 27 апреля. Продлите заранее, чтобы не прерывать доступ.
```

Это переиспользует существующий `<TrialBanner />` из `components/trial-banner.tsx`.

### 2. Сравнение тарифов (desktop) — ✅ сделано

На `sm:` и шире — кроме карточек показываем компактную таблицу:

| Что | 1 мес | 3 мес | 6 мес | 12 мес |
|---|---|---|---|---|
| Цена/мес | 499 ₽ | 499 ₽ | 399 ₽ | **299 ₽** |
| Итого | 499 ₽ | 1 497 ₽ | 2 394 ₽ | 3 588 ₽ |
| Экономия | — | — | 600 ₽ | 2 400 ₽ |
| До устройств | 1 | 2 | 2 | 3 |

На мобилке таблица скрывается (`hidden sm:block`), карточки делают ту же работу.

### 3. Flow после оплаты (WATA/YooMoney) — ✅ сделано

```
/plans/v2
   │
   ├── createInvoice(plan_id, devices, provider=wata, promo_code, auto_renew)
   │     → { payment_id, invoice_link }
   │
   ├── webApp.openLink(invoice_link)  (внешний браузер)
   │
   └── router.push(`/payment/pending?payment_id=${payment_id}`)
          │
          ├─ poll GET /payments/:id каждые 3 сек (макс 5 мин)
          ├─ если paid   → router.push('/')
          ├─ если failed → router.push('/payment/fail')
          └─ если pending > 5 мин → «проверим позже» + ссылка на /
```

### 4. A/B якорь (опционально в v2.1) — 🔲 TODO

Показывать **две цены** — «обычная» (× 1.4) и «реальная» — чтобы закрепить восприятие выгоды. Пока оставляем плейсхолдер `original_price?` в типе плана; включим, когда бэк начнёт отдавать.

---

## 🔲 TODO: бизнес-фичи (отложены)

Реализация этих фич удалена с фронта и не начиналась на бэке. UI-место для них в `/plans/v2` зарезервировано комментариями `// TODO(plans-v2)`. Возвращаемся, когда будут приоритеты на монетизацию.

### 🎟 Промокоды — 🔲 TODO (frontend + backend)

**Что нужно:**
- Поле «Есть промокод?» в `/plans/v2` перед выбором провайдера.
- Client-side валидация формата `^[A-Z0-9]{4,16}$`.
- Server-side валидация + применение при `POST /payments`.
- Показывать зачёркнутую полную цену + новую со скидкой + бейдж «−N%».

**Контракт бэкенда:**

```http
POST /api/v1/payments/promo/validate
  { "code": "FRIEND10", "plan_id": 2, "max_devices": 2 }
  → 200 { "valid": true, "discount_percent": 10, "description": "скидка 10% для друзей" }
  → 404 { "error": "promo_not_found" }
  → 410 { "error": "promo_expired" }
  → 429 { "error": "promo_usage_limit" }
```

И параметр `promo_code` в `POST /payments` — бэк сам пересчитывает итоговую сумму, фронт цифрам не доверяет.

**Схема БД (payment-service migration):**

```sql
CREATE TABLE promo_codes (
    id BIGSERIAL PRIMARY KEY,
    code VARCHAR(32) NOT NULL UNIQUE CHECK (code ~ '^[A-Z0-9]{4,32}$'),
    description TEXT NOT NULL DEFAULT '',
    discount_percent INT NOT NULL CHECK (discount_percent BETWEEN 1 AND 100),
    valid_from TIMESTAMPTZ,
    valid_until TIMESTAMPTZ,
    usage_limit_total INT,          -- NULL = безлимит
    usage_limit_per_user INT DEFAULT 1,
    used_count INT NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE promo_redemptions (
    id BIGSERIAL PRIMARY KEY,
    promo_code_id BIGINT NOT NULL REFERENCES promo_codes(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    payment_id BIGINT NOT NULL REFERENCES payments(id) ON DELETE CASCADE,
    discount_percent INT NOT NULL,
    discount_amount_rub DECIMAL(10,2) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (payment_id)
);
```

**Демо-коды для сидинга** (когда раскатим):
- `FRIEND10` → −10%, по 1 на юзера
- `NEWYEAR` → −15%, по 1 на юзера
- `DEVIN` → −20%, без лимита на юзера (тестовый)

**Фронт-план:**
- Вернуть `components/plans/PromoInput.tsx` (удалён в этой итерации) + импорт в `app/plans/v2/page.tsx`.
- Добавить обратно в `lib/api.ts`: `PromoValidationResult`, `validatePromoCode()`, `promoCode` в `CreateInvoiceOptions`.
- localStorage key `vpn_promo_code` — сохранять применённый код, проверять повторно при монтировании.

### 🔁 Автопродление — 🔲 TODO (frontend + backend)

**Что нужно:**
- Checkbox «продлевать автоматически» в `/plans/v2` рядом с оплатой.
- Дизейбл для Stars (разовые инвойсы) и YooMoney (offline-autopay требует сертификации).
- Поддержка WATA recurring flow (rebill) — передаём `auto_renew=true` при создании инвойса.
- Cron-воркер в subscription-service: за 24 ч до `expires_at` дергает payment-service чтобы списать.
- Ручка отмены `DELETE /api/v1/payments/auto-renew` — отключается на странице `/subscription`.

**Контракт бэкенда:**

- Колонка `subscriptions.auto_renew BOOLEAN NOT NULL DEFAULT FALSE` (новая миграция subscription-service).
- Колонка `payments.auto_renew BOOLEAN NOT NULL DEFAULT FALSE` — для аудита, какой платёж попросил автопродление.
- Параметр `auto_renew=true/false` в `POST /payments?provider=wata` — пробрасывается в WATA create invoice с `type: "Recurring"` (см. [docs/services/wata-integration.md](../services/wata-integration.md)).
- Webhook WATA recurring: отдельный обработчик в `payment-service/service/payment.go`, создаёт новый `payments` row и продлевает `subscriptions.expires_at`.
- Ручка отмены:

  ```http
  DELETE /api/v1/payments/auto-renew  (JWT)
  → 200 { "cancelled": true }
  ```

  Ставит `subscriptions.auto_renew = FALSE` для активной подписки юзера + (опц.) вызывает WATA `cancelRecurring`.

**Фронт-план:**
- Вернуть `components/plans/AutoRenewToggle.tsx` (удалён в этой итерации).
- В `lib/api.ts`: `autoRenew` в `CreateInvoiceOptions`, новый метод `cancelAutoRenew()`.
- localStorage key `vpn_auto_renew` — последний выбор пользователя.
- На `/subscription` page — блок «Автопродление включено • отключить» с вызовом `cancelAutoRenew()`.

**Edge cases, которые надо продумать:**
- Юзер сменил карту → первая попытка rebill падает → retry через сутки × 3 → уведомление в бота «продление не прошло».
- Юзер использовал промо на первый платёж → на автопродлении промо уже не применяется.
- Отмена за день до списания — cron не должен тиггернуть платёж.

---

## 🗂 Компонентная декомпозиция (текущее состояние)

```
app/
├─ plans/
│  ├─ page.tsx           ← v1 (не трогаем, fallback)
│  └─ v2/
│     └─ page.tsx        ← v2, orchestrator  ✅
└─ payment/
   └─ pending/
      └─ page.tsx        ← страница ожидания оплаты  ✅

components/
└─ plans/
   ├─ PlanCard.tsx        — карточка тарифа + badge + экономия       ✅
   ├─ DeviceSelector.tsx  — pill-группа «1/2/3 устройства»            ✅
   ├─ ProviderSelector.tsx — радио с учётом доступности               ✅
   ├─ PlanSkeleton.tsx    — 3-карточный skeleton loader               ✅
   ├─ CompareTable.tsx    — таблица-сравнение (desktop only)          ✅
   ├─ PromoInput.tsx      — 🔲 TODO: вернуть под промокоды
   └─ AutoRenewToggle.tsx — 🔲 TODO: вернуть под автопродление

lib/
├─ format.ts              — pluralize, formatRub, formatDuration, computeSavings, pricePerMonth  ✅
└─ api.ts                 — PaymentProvider type, getPaymentStatus, createInvoice({provider})     ✅
                            🔲 TODO: PromoValidationResult, validatePromoCode(), {promoCode, autoRenew} в CreateInvoiceOptions
```

---

## 🚧 Бэкенд-работы

Текущая итерация v2 **не требует бэкенд-изменений** — работает на существующих ручках.

Открытые работы для будущих итераций (каждое — отдельный под-тикет):

1. **Promo-коды** — см. секцию [🎟 Промокоды](#-промокоды--🔲-todo-frontend--backend). Статус: 🔲 TODO.
2. **Автопродление (WATA rebill)** — см. секцию [🔁 Автопродление](#-автопродление--🔲-todo-frontend--backend). Статус: 🔲 TODO.
3. **`GET /payments/:id`** — ручка статуса платежа. Сейчас `/payment/pending` фильтрует `listPayments(100)` — плохо масштабируется. Статус: 🔲 TODO (дешёвый тикет).
4. **`price_per_month`** в ответе `/subscriptions/plans/:id/pricing` — опц., чтобы фронт не считал руками. Статус: 🔲 TODO (опционально).

Метки в коде: `// TODO(plans-v2)` (фронт) и `// TODO(backend)` (Go).

---

## ✅ Критерии готовности (v2 MVP — текущий раунд)

Фронт — сделано:
- [x] `/plans/v2` показывает карточки + бейджи «Популярно» / «−N%».
- [x] Цена/мес считается и отображается.
- [x] Дефолт провайдера: `wata` если `!webApp.openInvoice`, иначе `telegram_stars`.
- [x] После `openLink` (WATA/YooMoney) — переход на `/payment/pending` с polling'ом.
- [x] Skeletons вместо full-screen loader.
- [x] Trial-бэйдж наверху страницы при активном триале.
- [x] Кнопка «Тарифы v2» с бейджем NEW на главной.
- [x] `npm run lint` и `tsc --noEmit` — чисто.
- [x] Мобильный layout (≤360px) не ломается.

Отложено (🔲 TODO — см. секции выше):
- [ ] Промокод: UI + бэкенд ручка + таблицы БД + сидинг демо-кодов.
- [ ] Автопродление: UI + WATA recurring + cron-воркер + ручка отмены.
- [ ] `GET /payments/:id` — заменить polling через `listPayments`.
- [ ] A/B якорь — «обычная» vs «реальная» цена.
- [ ] A/B счётчик конверсии v1 vs v2 (через posthog или наш analytics-log).
- [ ] Выбор локации как опция (upsell +99 ₽ за dedicated IP).

---

## 🔗 Полезные ссылки

- Текущая страница: [vpn_next/app/plans/page.tsx](../../../vpn_next/app/plans/page.tsx)
- API-клиент: [vpn_next/lib/api.ts](../../../vpn_next/lib/api.ts)
- WATA провайдер (Go): [vpn_go/services/payment-service/internal/provider/wata/wata.go](../../services/payment-service/internal/provider/wata/wata.go)
- Триал (фон): [05-trial-period.md](./05-trial-period.md)
