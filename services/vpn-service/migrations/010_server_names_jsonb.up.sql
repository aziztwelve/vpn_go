-- Convert vpn_servers.server_names from a single TEXT SNI to a JSONB array.
--
-- Why: Reality-friendly обход требует ротации SNI per VLESS-link. Один SNI
-- на сервер выдаёт фингерпринт «все коннекты с этого IP идут с одним и тем
-- же SNI» — детектируется поведенческими ML на DPI. С массивом SNI gateway
-- рандомит выбор при каждой выдаче subscription/VLESS-link, и юзер
-- последовательно цепляется к разным «cover-доменам» в одной и той же
-- /24 подсети сервера.
--
-- См. docs/tasks/end_sni.md (Stage 1) и docs/research/sni-pools.md.
--
-- Конверсия данных: TEXT 'github.com' → JSONB ["github.com"]. Строго
-- одноэлементный массив; админ потом обновляет вручную / через
-- deploy-xray-new.sh.

ALTER TABLE vpn_servers
    ALTER COLUMN server_names DROP DEFAULT,
    ALTER COLUMN server_names TYPE JSONB
        USING jsonb_build_array(server_names);

ALTER TABLE vpn_servers
    ALTER COLUMN server_names SET DEFAULT '["github.com"]'::jsonb;

COMMENT ON COLUMN vpn_servers.server_names IS
  'JSONB array of SNI candidates for Reality. Gateway picks random element per VLESS-link.';
