# add_server — runbook для подключения нового Xray-VPS

Этот каталог — шаблон/чеклист для **агента** (Devin или живого человека), которому
дали IP свежего сервера и сказали «добавь к нашим VPN». Цель — пройти по шагам
ниже сверху вниз, и в конце получить ещё один работающий Xray-инстанс в проде,
прописанный в `vpn_servers` и подхваченный `xray.Pool` в `vpn-core`.

Источник правды по архитектуре multi-server: <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/docs/services/multi-server.md" />

---

## 0. Что нужно от человека

| Параметр | Пример | Кто решает |
|---|---|---|
| `IP` нового VPS | `146.103.112.91` | человек |
| SSH-доступ туда (`root@IP` через ключ агента) | `~/.ssh/github_ed25519.pub` уже в `authorized_keys` | человек |
| Имя сервера в БД | `Netherlands-01` | спросить у человека (паттерн `Country-NN`) |
| Локация (город) | `Amsterdam` | агент может предложить через `curl ipapi.co/IP/json` |
| `country_code` (ISO-2) | `NL` | оттуда же |
| `server_max_connections` | `1000` (default) или `2000` | спросить если железо мощнее |
| Описание | `"Amsterdam, NL · vdsina"` | агент составит сам |

**Что агенту НЕ нужно спрашивать (хардкод, действует пока не сменили):**

- Backend (где `vpn-postgres`, `vpn-core`, `vpn-gateway`) = `178.104.217.201`. Агент работает **внутри** этого VPS — `docker exec vpn-postgres ...` доступен напрямую без ssh.
- Docker-сеть, в которой висит `vpn-core` = `vpn-stack_vpn`.
- gRPC `vpn-core` = `vpn-core:50062` (внутри сети).
- Inbound tag = `vless-reality-in`.
- Reality `dest` / `serverNames` = что в `deploy/scripts/deploy-xray-new.sh` дефолтом (на 2026-04-29 это `apple.com`).
- Файрвол: `:443` открыт всем, `:10085` (Xray gRPC API) — только для backend IP.

---

## 1. Проверка SSH-доступа

```bash
# При первом коннекте принять host key и убедиться что попадаем рутом.
ssh -o StrictHostKeyChecking=accept-new root@<IP> 'hostname; uname -m; cat /etc/os-release | grep -E "^(NAME|VERSION_ID)="; docker --version 2>/dev/null || echo NO_DOCKER'
```

Если `Permission denied (publickey,password)` — либо ключ не залит, либо
дефолтный `id_*` не подхватывается. Проверь явно:

```bash
ssh -i ~/.ssh/github_ed25519 root@<IP> 'true'
```

Если работает — добавь алиас в `~/.ssh/config`:

```
Host <IP>
  HostName <IP>
  User root
  IdentityFile ~/.ssh/github_ed25519
  IdentitiesOnly yes
```

Если ничего не помогает — **СТОП**, вернись к человеку: «зальёшь ключ
`ssh-ed25519 …github_ed25519.pub`?».

---

## 2. Прогон bootstrap-скрипта на новом VPS

Один скрипт ставит Docker, настраивает iptables (open `:443`, restrict `:10085` to
backend IP), сохраняет правила через `iptables-persistent`.

```bash
ssh root@<IP> 'bash -s' < add_server/prepare-vps.sh
```

В конце скрипт печатает финальные правила и `docker --version`. Если что-то
красное — разбираться, не идти дальше.

**Важные грабли:**
- `iptables -I DOCKER-USER ... -j DROP` потом `-I ... -j ACCEPT` ставит ACCEPT в начало (insert at top) и DROP во вторую строку — порядок инвертируется. Используй `-A` (append) **в правильном порядке**: сначала ACCEPT для backend, потом DROP catch-all. Скрипт делает это правильно.
- Docker 29.x по умолчанию **без userland-proxy** — трафик идёт через NAT/FORWARD, поэтому правил в `DOCKER-USER` достаточно. Для пущей паранойи скрипт **также** добавляет в `INPUT` (на случай если кто-то когда-то включит `userland-proxy: true` в `/etc/docker/daemon.json`).

---

## 3. Сгенерировать Reality keys + config.json

С backend (то есть **локально** для агента):

```bash
cd /root/.openclaw/workspace/vpn/vpn_go
./deploy/scripts/deploy-xray-new.sh \
    --name "<NAME>" \
    --location "<CITY>" \
    --country <CC> \
    --host "<IP>" \
    --port 443 \
    --max-conn <MAX> \
    --description "<HUMAN_READABLE>"
```

Скрипт:
1. Запускает `docker run --rm ghcr.io/xtls/xray-core x25519` → PrivateKey + PublicKey.
2. Генерит `short_id` через `openssl rand -hex 8`.
3. Кладёт `config.json` в `deploy/compose/xray-new/<NAME>/` (этот каталог **в `.gitignore`** — приватники не утекут).
4. Печатает готовые шаги (scp / docker run / SQL / grpcurl).

**Сохрани вывод** — оттуда нужны PublicKey, ShortID, и SQL для следующего шага.

---

## 4. Развернуть Xray на новом VPS

```bash
NAME_DIR=$(echo "<NAME>" | tr -cs 'A-Za-z0-9' '_')
ssh root@<IP> 'mkdir -p /opt/xray'
scp deploy/compose/xray-new/${NAME_DIR}/config.json root@<IP>:/opt/xray/config.json
ssh root@<IP> 'docker run -d --name xray --restart unless-stopped \
    -v /opt/xray/config.json:/etc/xray/config.json:ro \
    -p 443:443 -p 10085:10085 \
    ghcr.io/xtls/xray-core:latest -c /etc/xray/config.json'
```

Проверка:

```bash
ssh root@<IP> 'sleep 3; docker ps --format "{{.Names}}\t{{.Status}}"; docker logs xray 2>&1 | tail -10'
# Ожидание: "Xray X.Y.Z started", listening 0.0.0.0:443 и 0.0.0.0:10085.
```

Снаружи бэкенда (с любой машины кроме него):
- `:443` должен быть **открыт** (TCP handshake проходит).
- `:10085` должен быть **closed/timeout** (DOCKER-USER DROP).

С самого backend:
- `:10085` должен быть **открыт** (DOCKER-USER ACCEPT для `178.104.217.201`).

Грабли: если `bash -c '</dev/tcp/<IP>/10085'` запускаешь **с backend'а** — увидишь
«open», т.к. источник = `178.104.217.201`. Это **корректно**. Чтобы проверить «извне»,
запусти проверку **с другого VPS**, например с самого нового:
`ssh root@<IP> "timeout 3 bash -c '</dev/tcp/<IP>/10085'"` — это connect через
public интерфейс из другой стороны и попадает в catch-all DROP.

---

## 5. INSERT в `vpn_servers`

```bash
docker exec -i vpn-postgres psql -U vpn -d vpn <<SQL
INSERT INTO vpn_servers (
    name, location, country_code, host, port,
    public_key, private_key, short_id, dest, server_names,
    xray_api_host, xray_api_port, inbound_tag, is_active,
    server_max_connections, description
) VALUES (
    '<NAME>','<CITY>','<CC>','<IP>',443,
    '<PUBLIC_KEY>','<PRIVATE_KEY>','<SHORT_ID>','apple.com:443','apple.com',
    '<IP>',10085,'vless-reality-in',true,
    <MAX>,'<DESCRIPTION>'
) RETURNING id;
SQL
```

**Запиши `id`** из RETURNING — он нужен для ResyncServer.

Проверка:

```bash
docker exec vpn-postgres psql -U vpn -d vpn -c \
  "SELECT id, name, host, port, is_active, server_max_connections FROM vpn_servers ORDER BY id;"
```

**Грабля по schema:** в `deploy/schema.sql` `server_names` объявлен `JSONB`, но
**в проде это `text`** с одиночной строкой (не массив). SQL выше работает как
есть — не пытайся `'["apple.com"]'::jsonb`, упадёт.

---

## 6. Resync — прописать всех существующих юзеров на новый Xray

```bash
docker run --rm --network vpn-stack_vpn fullstorydev/grpcurl:latest \
    -plaintext -d '{"server_id": <ID>}' \
    vpn-core:50062 vpn.v1.VPNService/ResyncServer
```

Ожидаемый ответ: `{"usersTotal": N, "usersAdded": N}` (либо
`usersAlready: N` если эти юзеры уже были, что норма после рестарта Xray).

**Когда обязательно дёргать вручную:**
- Сразу после INSERT нового сервера.
- После любого `docker restart xray` (Xray держит `clients[]` in-memory; рестарт = всё в ноль). Есть `ResyncCron` в `vpn-core` который сам это починит, но руками быстрее.

---

## 7. Финальная проверка

```bash
# 1. Heartbeat должен видеть новый сервер
docker logs vpn-core 2>&1 | grep heartbeat | tail -3
# Должно быть servers_checked: <N+1> (было N, стало N+1)

# 2. БД отдаёт сервер во view
docker exec vpn-postgres psql -U vpn -d vpn -c \
  "SELECT id, name, host, port, is_active, load_percent FROM vpn_servers WHERE id=<ID>;"

# 3. Xray на новом VPS получает реальные клиенты по :443 (когда юзеры рефрешат подписку)
ssh root@<IP> 'docker logs xray 2>&1 | tail -20'
# Ожидание: "REALITY: ..." коннекты с разных IP. "server name mismatch" из bot/scanner — норма.
```

---

## 8. Записать в memory + закоммитить (если есть изменения в репо)

В `/root/.openclaw/workspace/memory/YYYY-MM-DD.md` короткую запись:

```markdown
## <NAME> (<IP>) — добавлен в vpn_go (id=<ID>)

- Провайдер: <vdsina/Hetzner/...>, <Country>, <City>, <OS>
- Reality dest/SNI: apple.com (по дефолту скрипта)
- PublicKey: ...
- ShortID: ...
- Resync: usersAdded=N, failed=0
- servers_checked: N+1 ✓

### Какие файлы тронул в репо
- (если ничего — ничего не коммить)
```

Если меняли `add_server/` или `deploy/scripts/deploy-xray-new.sh` —
один коммит:

```
chore(deploy): <что сделал>

Generated with [Devin](https://cli.devin.ai/docs)
Co-Authored-By: Devin <158243242+devin-ai-integration[bot]@users.noreply.github.com>
```

**НЕ пушить без явной просьбы человека.**

---

## Ссылки

- Архитектура multi-server: <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/docs/services/multi-server.md" />
- Скрипт генерации Xray VPS: <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/deploy/scripts/deploy-xray-new.sh" />
- Bootstrap нового VPS (Docker + iptables): <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/add_server/prepare-vps.sh" />
- xray.Pool: <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/platform/pkg/xray/pool.go" />
- ResyncServer handler: <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/services/vpn-service/internal/api/vpn.go" />
