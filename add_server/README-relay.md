# add_server (relay) — runbook для подключения каскадного relay-VPS

Этот каталог покрывает два типа узлов:

| Тип узла | runbook | bootstrap | Зачем |
|---|---|---|---|
| **exit** (полный Xray VLESS+Reality) | [`README.md`](README.md) | [`prepare-vps.sh`](prepare-vps.sh) | Финальная нода — расшифровывает Reality и идёт в интернет. |
| **relay** (TCP forward через `dokodemo-door`) | этот файл | [`prepare-relay-vps.sh`](prepare-relay-vps.sh) | Точка входа в РФ для каскадной схемы. Не знает UUID, переливает байты в exit. |

---

## Архитектура каскада

```
Юзер (РФ)            RU-relay (этот узел)        Exit-узел (FI/NL/DE)
                     dokodemo-door :8443         xray VLESS+Reality :8443
   │                        │                           │
   │ Reality SNI=...        │ TCP raw forward           │
   ├───────────────────────▶├──────────────────────────▶├──▶ freedom → internet
   │ host=<RU_IP>           │                           │
   │                        │                           ▲
                                                        │ gRPC AddUser/RemoveUser
                                                Backend (178.104.217.201)
```

**Ключевой инсайт**: relay-узел ничего не знает про юзеров. Он не управляется
бэкендом, у него нет gRPC API на :10085. Бэкенд продолжает ходить за управлением
напрямую на exit-узел. Reality-хендшейк проедет через relay побайтово и
завершится на exit. С точки зрения exit — клиент пришёл с RU-IP. С точки зрения
юзера — он коннектится в РФ, FI-IP вообще не светится в его сетевом стеке.

В `vpn_servers` каскадная запись хранится так:

| поле | значение | смысл |
|---|---|---|
| `host` | `<RU_IP>` | куда юзер шлёт Reality-хендшейк |
| `port` | `8443` | то же |
| `public_key` / `short_id` / `dest` / `server_names` | **те же что у exit** | потому что хендшейк завершится на exit |
| `xray_api_host` | `<EXIT_IP>` (FI/NL/DE) | бэкенд продолжает управлять exit-узлом |
| `xray_api_port` | `10085` | то же |
| `private_key` | приватник exit (формальная копия) | xray-сервер на relay его не использует |
| `name` | `<ExitName>-via-RU` | соглашение по нейминг'у |
| `country_code` | страна **exit** (FI/NL/DE), а не RU | потому что трафик из интернета идёт оттуда — флаг в Happ должен соответствовать реальному выходу |

---

## 0. Что нужно от человека

| Параметр | Пример | Кто решает |
|---|---|---|
| `RU_IP` нового VPS | `91.184.245.196` | человек |
| SSH-доступ туда (`root@RU_IP` через ключ агента) | `~/.ssh/github_ed25519.pub` уже в `authorized_keys` | человек заливает ключ |
| Имя записи в БД | `Finland-01-via-RU` | агент составляет: `<exit.name>-via-RU` |
| Какой exit-узел каскадируем | `Finland` (id=91) | человек |

---

## 1. Проверка SSH

```bash
ssh -o StrictHostKeyChecking=accept-new root@<RU_IP> 'hostname; uname -m; . /etc/os-release && echo "$NAME $VERSION_ID"; docker --version 2>/dev/null || echo NO_DOCKER'
```

Если `Permission denied (publickey,password)` — ключ не залит. Дай человеку
публичный ключ агента (`~/.ssh/github_ed25519.pub`) и попроси добавить в
`/root/.ssh/authorized_keys` на RU-VPS. Без этого дальше не пройти.

Добавь алиас в `~/.ssh/config` для удобства:

```
Host ru01 <RU_IP>
  HostName <RU_IP>
  User root
  IdentityFile ~/.ssh/github_ed25519
  IdentitiesOnly yes
```

---

## 2. Bootstrap нового RU-VPS

```bash
ssh root@<RU_IP> 'bash -s' < add_server/prepare-relay-vps.sh
```

Скрипт ставит Docker и iptables-persistent, открывает :22 и :8443. **Не
открывает** :10085 (gRPC API на relay не нужен).

---

## 3. Развернуть xray-relay

Достаём актуальный exit (host:port) из БД:

```bash
docker exec vpn-postgres psql -U vpn -d vpn -At -c \
  "SELECT host || ':' || port FROM vpn_servers WHERE id = <EXIT_ID>;"
# Например: 204.168.248.33:8443
```

Создаём конфиг локально:

```bash
EXIT_HOST_PORT="204.168.248.33:8443"  # подставь свой
EXIT_HOST="${EXIT_HOST_PORT%:*}"
EXIT_PORT="${EXIT_HOST_PORT#*:}"

cat > /tmp/relay-config.json <<EOF
{
  "log": {"loglevel": "warning"},
  "inbounds": [{
    "tag": "relay-in",
    "listen": "0.0.0.0",
    "port": 8443,
    "protocol": "dokodemo-door",
    "settings": {
      "address": "${EXIT_HOST}",
      "port": ${EXIT_PORT},
      "network": "tcp",
      "followRedirect": false
    }
  }],
  "outbounds": [{"protocol": "freedom", "tag": "direct"}]
}
EOF
```

Заливаем и запускаем:

```bash
ssh root@<RU_IP> 'mkdir -p /opt/xray'
scp /tmp/relay-config.json root@<RU_IP>:/opt/xray/config.json
ssh root@<RU_IP> 'docker run -d --name xray --restart unless-stopped \
    -v /opt/xray/config.json:/etc/xray/config.json:ro \
    -p 8443:8443 \
    ghcr.io/xtls/xray-core:latest -c /etc/xray/config.json'
```

Проверка:

```bash
ssh root@<RU_IP> 'sleep 3; docker ps --format "{{.Names}}\t{{.Status}}"; docker logs xray 2>&1 | tail -10'
# Ожидание: "Xray X.Y.Z started", listening 0.0.0.0:8443.
```

С локалки (или с любой машины с доступом):

```bash
# TCP-handshake до RU-relay должен пройти
timeout 3 bash -c '</dev/tcp/<RU_IP>/8443' && echo OK || echo FAIL

# А ещё лучше — Reality-хендшейк через relay должен резолвиться на exit.
# Создай тестовый xray client config с address=<RU_IP> и проверь:
curl --proxy socks5h://127.0.0.1:10808 https://ifconfig.me
# Ответ должен быть IP exit-узла (например 204.168.248.33), НЕ <RU_IP>.
```

---

## 4. INSERT в `vpn_servers`

Копируем Reality-параметры с exit-узла на новую запись:

```bash
docker exec -i vpn-postgres psql -U vpn -d vpn <<SQL
INSERT INTO vpn_servers (
    name, location, country_code, host, port,
    public_key, private_key, short_id, dest, server_names,
    xray_api_host, xray_api_port, inbound_tag, is_active,
    server_max_connections, description
)
SELECT
    name || '-via-RU' AS name,
    location || ' (вход через РФ)' AS location,
    country_code,                                 -- флаг exit-страны
    '<RU_IP>' AS host,                            -- ← клиент шлёт сюда
    port,                                         -- 8443
    public_key, private_key, short_id, dest, server_names,
    xray_api_host, xray_api_port,                 -- API остаётся exit:10085
    inbound_tag, is_active,
    server_max_connections,
    'Каскад: вход через РФ (' || '<RU_IP>' || ') → выход в ' || country_code AS description
FROM vpn_servers
WHERE id = <EXIT_ID>
RETURNING id, name, host, xray_api_host;
SQL
```

UNIQUE-constraint `idx_vpn_servers_host_port` пропустит — `(<RU_IP>, 8443)` уникален.

**Запиши `id`** из RETURNING — нужно для следующего шага.

---

## 5. ResyncServer на новый ID

Поскольку `xray_api_host` указывает на exit-узел, ResyncServer вызовет `AddUser`
на exit. Юзеры там уже есть — получим `usersAlready: N`. Это норма (см.
[`README.md`](README.md#6-resync) шаг 6).

```bash
docker run --rm --network vpn-stack_vpn fullstorydev/grpcurl:latest \
    -plaintext -d '{"server_id": <NEW_ID>}' \
    vpn-core:50062 vpn.v1.VPNService/ResyncServer
```

Зачем дёргать если юзеры уже на exit? Чтобы heartbeat (`ResyncCron`) не разъехался
в первые часы — он сам всё подтянет, но руками быстрее и сразу видно «работает / не
работает».

---

## 6. Финальная проверка

```bash
# 1. БД отдаёт обе записи (exit + relay)
docker exec vpn-postgres psql -U vpn -d vpn -c \
  "SELECT id, name, country_code, host, port, xray_api_host, is_active
   FROM vpn_servers ORDER BY id;"

# 2. Подписка отдаёт оба сервера в base64-листе
SUB_TOKEN=$(docker exec vpn-postgres psql -U vpn -d vpn -At -c \
    "SELECT subscription_token FROM vpn_users LIMIT 1;")
curl -s "https://cdn.osmonai.com/api/v1/subscription/${SUB_TOKEN}" | base64 -d
# Должно содержать VLESS-ссылку с address=<RU_IP> отдельной строкой.

# 3. На relay-VPS видны коннекты, на exit — source=<RU_IP>
ssh root@<RU_IP> 'docker logs xray 2>&1 | tail -20'
ssh root@<EXIT_IP> 'docker logs xray 2>&1 | tail -20'
```

---

## 7. Подводные камни

1. **Если RU-IP попадёт в реестр РКН** — упадёт каскадная цепочка. Старая
   прямая ссылка на exit продолжит работать. Юзер переключится в Happ одним
   тапом. Держи 2-3 RU-IP в горячем резерве.

2. **Latency**: добавится RU↔exit RTT (обычно 30-60ms). Юзеры это заметят —
   поэтому каскадная запись существует **в дополнение** к прямой, а не вместо
   неё.

3. **Логи на relay**: `dokodemo-door` не пишет payload, но IP-ы клиентов видны
   при `loglevel: info`. Поэтому `prepare-relay-vps.sh` ставит `warning`.

4. **UDP**: Reality поверх TCP, UDP forward не нужен. В конфиге `network: tcp`.

5. **TCP Fast Open / mptcp**: НЕ включай — `dokodemo-door` может глючить.

6. **Whitelist exit:8443 только для RU-IP** — НЕ делай пока каскад в тесте.
   Это сломает прямую ссылку на exit для тех юзеров, кто остался на ней. Когда
   убедишься что каскад стабилен и юзеров перевели — можно: `iptables -I
   DOCKER-USER -p tcp --dport 8443 ! -s <RU_IP_LIST> -j DROP`.

---

## 8. Записать в memory и закоммитить

```bash
cd /root/.openclaw/workspace/vpn/vpn_go
git status
git add deploy/compose/xray-new/<RelayName>/  # если ты сохранил конфиг
git commit -m "Add cascade relay <RelayName> via RU (exit: <ExitName>)"
```

Записать в `memory/YYYY-MM-DD.md` (workspace):
- IP relay-VPS, дата деплоя
- exit-сервер с которого скопированы Reality-параметры
- любые провайдер-специфичные грабли (firewall, iptables, и т.д.)
