-- Воронки (campaigns) для блогеров и партнёров.
--
-- Сущность отличается от персональной реф-ссылки (referral_links): её владелец —
-- админ, она может вообще не быть привязана к юзеру. Используется как UTM-кампания
-- с deep-link'ом https://t.me/<bot>?start=src_<slug>.
--
-- First-touch attribution: когда новый юзер регистрируется, он один раз
-- привязывается к кампании через user_attribution и больше эта связь не меняется.
-- Это позволяет считать LTV/revenue по кампании JOIN'ом с payments.
--
-- ALTER bot_starts добавляем здесь же (референц на campaigns(id)), хотя сама
-- таблица создана в auth-service. БД общая, а порядок миграций (auth → ... → referral)
-- гарантирует что bot_starts уже существует.

-- 1. Сами кампании.
CREATE TABLE IF NOT EXISTS campaigns (
    id BIGSERIAL PRIMARY KEY,
    -- Slug: что-то типа "ivan_jan2026". Длина ≤ 60 чтобы start-параметр Telegram
    -- (max 64) уместился с префиксом "src_".
    slug VARCHAR(60) NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9_-]{3,60}$'),
    name VARCHAR(255) NOT NULL,
    notes TEXT,
    -- Если задан — % с оплат рефералов кампании уходит ему на users.balance.
    partner_user_id BIGINT REFERENCES users(id),
    -- 0..50%. NULL = без выплат (только аналитика).
    payout_percent INT CHECK (payout_percent BETWEEN 0 AND 50),
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_by BIGINT NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived_at TIMESTAMPTZ,
    -- Если выставлен процент — нужен и партнёр-получатель.
    CHECK ((payout_percent IS NULL) OR (partner_user_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_campaigns_active ON campaigns(is_active) WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_campaigns_partner ON campaigns(partner_user_id) WHERE partner_user_id IS NOT NULL;

COMMENT ON TABLE  campaigns                   IS 'Маркетинговые воронки/кампании (deep-link ?start=src_<slug>)';
COMMENT ON COLUMN campaigns.slug              IS 'Читаемый идентификатор для start-параметра ([a-z0-9_-]{3,60})';
COMMENT ON COLUMN campaigns.partner_user_id   IS 'Получатель %-выплат (только если payout_percent IS NOT NULL)';
COMMENT ON COLUMN campaigns.payout_percent    IS '% с оплат рефералов кампании на users.balance партнёра. NULL = без выплат';
COMMENT ON COLUMN campaigns.archived_at       IS 'Кампания заархивирована: новые /start не атрибутируются, старая статистика остаётся';

-- 2. Pending атрибуция: telegram_id → campaign_id, до того как юзер открыл Mini App.
-- Аналог pending_referrals. Заполняется gateway/auth при /start src_<slug>,
-- съедается auth-service'ом в ValidateTelegramUser для нового юзера.
CREATE TABLE IF NOT EXISTS pending_campaigns (
    telegram_id BIGINT PRIMARY KEY,
    campaign_id BIGINT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_pending_campaigns_created_at ON pending_campaigns(created_at);

COMMENT ON TABLE pending_campaigns IS 'Атрибуция к кампании, ожидающая первой регистрации юзера в Mini App';

-- 3. Финальная атрибуция (first-touch, неизменна).
-- Заполняется в ValidateTelegramUser единожды при создании users-record.
CREATE TABLE IF NOT EXISTS user_attribution (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    campaign_id BIGINT NOT NULL REFERENCES campaigns(id),
    attributed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_user_attribution_campaign ON user_attribution(campaign_id);

COMMENT ON TABLE user_attribution IS 'Привязка юзера к кампании (first-touch, неизменна после создания)';

-- 4. Журнал партнёрских выплат по кампаниям. Идемпотентность по payment_id
-- (UNIQUE) — один платёж = одна выплата по кампании, чтобы webhook-ретраи
-- не двоили начисления.
CREATE TABLE IF NOT EXISTS campaign_payouts (
    id BIGSERIAL PRIMARY KEY,
    campaign_id BIGINT NOT NULL REFERENCES campaigns(id),
    partner_user_id BIGINT NOT NULL REFERENCES users(id),
    invited_user_id BIGINT NOT NULL REFERENCES users(id),
    payment_id BIGINT NOT NULL UNIQUE,
    amount DECIMAL(12,2) NOT NULL CHECK (amount > 0),
    is_applied BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_campaign_payouts_campaign ON campaign_payouts(campaign_id);
CREATE INDEX IF NOT EXISTS idx_campaign_payouts_partner ON campaign_payouts(partner_user_id);

COMMENT ON TABLE campaign_payouts IS 'Партнёрские выплаты по кампаниям (идемпотентны по payment_id)';

-- 5. Расширяем bot_starts для удобной агрегации /start метрики по кампании.
-- Колонка опциональна (NULL = клик не по src_-ссылке).
ALTER TABLE bot_starts ADD COLUMN IF NOT EXISTS campaign_id BIGINT REFERENCES campaigns(id);
CREATE INDEX IF NOT EXISTS idx_bot_starts_campaign ON bot_starts(campaign_id) WHERE campaign_id IS NOT NULL;

COMMENT ON COLUMN bot_starts.campaign_id IS 'Кампания, от которой пришёл /start (NULL = не по src_-ссылке)';
