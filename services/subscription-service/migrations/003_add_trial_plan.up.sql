-- is_trial флаг помечает план как "триальный". Такие планы не показываются
-- в UI как покупаемые (WHERE is_trial=false в ListPlans), но используются
-- сервисом для StartTrial.
ALTER TABLE subscription_plans
    ADD COLUMN IF NOT EXISTS is_trial BOOLEAN NOT NULL DEFAULT false;

-- Seed: триал-план (3 дня, 1 устройство, price=0, отдельный id=99 чтобы
-- не мешать нумерации обычных планов 1-4).
INSERT INTO subscription_plans (id, name, duration_days, max_devices, base_price, price_stars, is_active, is_trial)
VALUES (99, 'Пробный период', 3, 1, 0.00, 0, true, true)
ON CONFLICT (id) DO NOTHING;

-- Статус 'trial' добавляется в check-constraint subscriptions.status только
-- если такая проверка вообще есть. Сейчас status — просто varchar(50) без
-- ограничений (см. 001_create_subscriptions.up.sql), значит 'trial' уже
-- разрешён. Оставляем как есть — если в будущем добавим CHECK, не забыть
-- про 'trial'.
