-- promo_codes — одноразовые персональные промо-ссылки.
--
-- Использование:
--   1. Админ в боте: /promo @username
--      → auth-service.PromoService.Issue(user_id, plan_id=101)
--      → INSERT promo_codes (token=random64, user_id, plan_id, expires_at=NOW()+30d)
--      → бот шлёт юзеру персональное сообщение с inline-кнопкой
--        url = https://cdn.osmonai.com/promo/p/<token>
--
--   2. Юзер кликает → gateway GET /promo/p/{token} (PUBLIC, no JWT):
--      → auth-service.PromoService.Redeem(token)
--      → если active (used_at IS NULL, expires_at > NOW()) →
--          payment-service.CreateInvoice(user_id, plan_id, max_devices, platega)
--          UPDATE promo_codes SET payment_id=<id>
--          → 302 → invoice.invoice_link (Platega)
--
--   3. Оплата прошла → payment-webhook → если payment связан с promo →
--      auth-service.PromoService.MarkUsed(payment_id) → SET used_at=NOW().
--
-- Двойной клик до оплаты — идемпотентен: возвращаем тот же invoice_link.
-- Двойная оплата — невозможна на стороне Platega (одна сессия = один payment).

CREATE TABLE IF NOT EXISTS promo_codes (
    id          BIGSERIAL PRIMARY KEY,

    -- Случайный URL-safe токен 32 байта = 64 hex-символа.
    -- Генерируется в auth-service crypto/rand. UNIQUE-индекс гарантирует,
    -- что коллизий не будет (ловим INSERT ON CONFLICT и регенерим).
    token       VARCHAR(64) NOT NULL UNIQUE,

    -- Кому выдан промо. Жёсткая привязка: даже если юзер перешлёт ссылку —
    -- payment создаётся на этого user_id, не на кликнувшего. Friendly-fire
    -- защищены от воровства скидки.
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- Какой план активируется. Сейчас всегда 101 (Промо 79₽), но
    -- параметризовано на будущее (могут быть разные акции).
    plan_id     INTEGER NOT NULL,

    -- Сколько устройств. Default 2 — соответствует pricing'у плана 101.
    max_devices INTEGER NOT NULL DEFAULT 2,

    -- Кто выдал (admin user_id). Для аудита.
    created_by  BIGINT REFERENCES users(id) ON DELETE SET NULL,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Срок жизни промо. NULL = бессрочно (не используем сейчас).
    -- Default Issue-логика: NOW() + INTERVAL '30 days'.
    expires_at  TIMESTAMPTZ,

    -- Когда юзер оплатил. NULL = ещё не использован, любой клик создаёт
    -- payment с тем же promo_id (idempotent через payment_id).
    used_at     TIMESTAMPTZ,

    -- Какой платёж создан по этому промо (для idempotent retry на клике
    -- и для MarkUsed по webhook'у). NULL до первого клика.
    payment_id  BIGINT
);

-- Один активный (не used) промо на (user, plan) — UNIQUE гарантирует, что
-- нельзя двойной /promo на того же юзера с тем же планом, пока первый не
-- использован или не expired (тогда снять флаг через DELETE/UPDATE и
-- выпустить новый).
--
-- Сделано через partial UNIQUE index чтобы used_at IS NULL коды не
-- мешали друг другу с used_at IS NOT NULL.
CREATE UNIQUE INDEX IF NOT EXISTS uniq_promo_codes_active
    ON promo_codes(user_id, plan_id)
    WHERE used_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_promo_codes_user
    ON promo_codes(user_id);

CREATE INDEX IF NOT EXISTS idx_promo_codes_payment
    ON promo_codes(payment_id)
    WHERE payment_id IS NOT NULL;

COMMENT ON TABLE promo_codes IS
    'Персональные промо-токены для целевых скидок. Раздаются через бот-команду /promo.';
COMMENT ON COLUMN promo_codes.token IS
    '64 hex-символа. URL: https://cdn.osmonai.com/promo/p/<token>';
COMMENT ON COLUMN promo_codes.payment_id IS
    'Заполняется при первом клике (CreateInvoice). Idempotent на повторные клики.';
COMMENT ON COLUMN promo_codes.used_at IS
    'Заполняется webhook-handler''ом payment-service когда платёж paid.';
