-- Revert: take first element of JSONB array as the single TEXT value.
-- ⚠️ Lossy: any extra SNI beyond [0] are lost irreversibly.

ALTER TABLE vpn_servers
    ALTER COLUMN server_names DROP DEFAULT,
    ALTER COLUMN server_names TYPE TEXT
        USING (server_names->>0);

ALTER TABLE vpn_servers
    ALTER COLUMN server_names SET DEFAULT 'github.com';
