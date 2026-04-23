-- server_max_connections — максимальное число одновременных юзеров
-- которое держит один Xray-inbound (эмпирически; зависит от железа VPS).
-- Используется cron'ом для вычисления load_percent и балансировкой UI.
-- Для dev default = 1000, на проде можно поднять до 5000-10000.
ALTER TABLE vpn_servers
    ADD COLUMN IF NOT EXISTS server_max_connections INTEGER NOT NULL DEFAULT 1000
    CHECK (server_max_connections > 0);

-- description — человекочитаемое описание (для UI "Берлин, DE · 10 Gbit/s").
ALTER TABLE vpn_servers
    ADD COLUMN IF NOT EXISTS description VARCHAR(255) NOT NULL DEFAULT '';
