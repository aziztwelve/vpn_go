-- 013: убираем тест-план id=100 («Тест 1₽» → 3₽, миграции 008/009) из
-- видимости пользователей. Хард-DELETE невозможен из-за FK
-- subscriptions.plan_id → subscription_plans(id) (NO CASCADE, см. 001),
-- поэтому soft-disable через is_active=false:
--   - ListPlans(activeOnly=true) сразу перестаёт его возвращать (см.
--     subscription-service/internal/repository/subscription.go)
--   - бот /buy и Mini App /plans автоматически перестают его показывать
--   - историю подписок тестовых юзеров на этом плане не ломаем
--
-- device_addon_pricing для plan_id=100 удаляем — там нет внешних ссылок,
-- они не нужны для disabled-плана.
UPDATE subscription_plans SET is_active = false WHERE id = 100;
DELETE FROM device_addon_pricing WHERE plan_id = 100;
