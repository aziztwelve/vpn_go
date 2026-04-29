-- Реферальная программа.
-- Таблицы используют users(id) — внутренний BIGINT primary key.

-- 1. Реферальные ссылки. Один юзер = один токен (UNIQUE user_id).
CREATE TABLE IF NOT EXISTS referral_links (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    token VARCHAR(32) NOT NULL UNIQUE,
    click_count INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_referral_links_token ON referral_links(token);

COMMENT ON TABLE referral_links IS 'Уникальные реферальные ссылки пользователей (1 юзер = 1 токен)';

-- 2. Связи "пригласитель → приглашённый". Один invited может иметь
--    только одного inviter (PK на invited_id фактически — но мы используем
--    composite PK для совместимости со схемой из deploy/schema.sql).
--    UNIQUE(invited_id) гарантирует anti-abuse: нельзя перезаписать
--    inviter'а.
CREATE TABLE IF NOT EXISTS referral_relationships (
    inviter_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    invited_id BIGINT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    status VARCHAR(20) NOT NULL DEFAULT 'registered',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (inviter_id, invited_id),
    CHECK (inviter_id <> invited_id)
);

CREATE INDEX IF NOT EXISTS idx_referral_relationships_inviter ON referral_relationships(inviter_id);

COMMENT ON TABLE referral_relationships IS 'Связь "кто кого пригласил". UNIQUE(invited_id) — один inviter на юзера';
COMMENT ON COLUMN referral_relationships.status IS 'registered → пользователь только зарегистрировался; purchased → совершил первую оплату';

-- 3. Журнал начисления бонусов. Каждая запись — конкретный бонус
--    (либо days, либо balance). PaymentID нужен для идемпотентности
--    при ApplyBonus: один платёж = один бонус.
CREATE TABLE IF NOT EXISTS referral_bonuses (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    invited_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    bonus_type VARCHAR(20) NOT NULL CHECK (bonus_type IN ('days', 'balance')),
    days_amount INT,
    balance_amount DECIMAL(12,2),
    payment_id BIGINT,
    is_applied BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_referral_bonuses_user_id ON referral_bonuses(user_id);
CREATE INDEX IF NOT EXISTS idx_referral_bonuses_invited_user_id ON referral_bonuses(invited_user_id);
-- Уникальный индекс по payment_id для идемпотентности (один платёж = один бонус).
-- WHERE payment_id IS NOT NULL — потому что при регистрации payment_id=NULL.
CREATE UNIQUE INDEX IF NOT EXISTS uniq_referral_bonuses_payment_id
    ON referral_bonuses(payment_id) WHERE payment_id IS NOT NULL;

COMMENT ON TABLE referral_bonuses IS 'Бонусы реферальной программы. payment_id для идемпотентности';
COMMENT ON COLUMN referral_bonuses.user_id IS 'Кому начислен бонус (inviter или invited)';
COMMENT ON COLUMN referral_bonuses.invited_user_id IS 'Какой приглашённый принёс этот бонус';
COMMENT ON COLUMN referral_bonuses.bonus_type IS 'days — продление подписки; balance — пополнение партнёрского баланса';
