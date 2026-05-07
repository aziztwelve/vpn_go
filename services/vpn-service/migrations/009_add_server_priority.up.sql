-- 009_add_server_priority.up.sql
-- Колонка priority определяет «приоритетный блок» в выдаче подписки:
-- сразу после первого режима «⚡ Обычный VPN» эмитятся серверы с priority>0
-- (отсортированные ASC по priority — меньшее число выше). После них идут
-- режимы «🚀 Обход блокировок» и «🎬 YouTube», затем — обычные серверы (priority=0).
--
-- Идея: подсветить юзеру «специальные» опции (LTE-обход, каскад через РФ)
-- которые помогают в проблемных сетях, до того как он начнёт листать список
-- географий. См. handler/subscription_config.go writeBase64Format/JSONFormat.
ALTER TABLE vpn_servers
    ADD COLUMN priority INTEGER NOT NULL DEFAULT 0;

COMMENT ON COLUMN vpn_servers.priority IS
    'Приоритет в подписке: 0 = обычный сервер; >0 = «приоритетный», эмитится сразу после первого режима. Меньшее число — выше в списке.';

CREATE INDEX idx_vpn_servers_priority ON vpn_servers (priority);
