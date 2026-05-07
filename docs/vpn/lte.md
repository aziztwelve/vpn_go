# LTE-обход (RU-SNI inbound)

Как обходить РКН-блокировки в регионах с **белым DPI-списком** (когда оператор пропускает только заранее одобренные SNI). Кейс: Республика Хакасия, г. Абаза — «отключен мобильный интернет» = по факту работает только whitelist разрешённых РУ-сайтов.

**Статус реализации:** ✅ **в проде с 2026-05-07** на `178.105.1.202` (Hetzner Falkenstein), порт `:1443`, SNI `ads.x5.ru`.

> ⚠️ **Известное ограничение (2026-05-07):** на некоторых LTE-операторах с
> агрессивным DPI (подтверждено: Хакасия, юзер `@Tapdyg1`) Reality-handshake
> через **Hetzner DE IP-pool** не пробивается, несмотря на whitelist-SNI.
> DPI блочит трафик по dest-IP к иностранным AS как класс. Решение —
> поднять параллельный inbound на VPS **внутри РФ** (Yandex.Cloud / Selectel),
> у конкурентов работает именно так. Спека и план: [tasks/17-ru-vps-lte.md](../tasks/17-ru-vps-lte.md).

> Контекст: к весне 2026 РКН/региональные операторы стали выкатывать «белые списки» SNI — пропускают только заведомо российские домены, всё остальное (включая `apple.com`, `google.com`, и наш стандартный SNI) дропают. Reality с зарубежным SNI перестал работать для части юзеров. LTE-инбаунд использует SNI **российского** ритейл-сайта (`ads.x5.ru` — рекламный поддомен X5 Retail Group) — DPI пропускает как «свой» трафик, а внутри идёт VLESS.

---

## 🧠 Простыми словами

Reality — это TLS-протокол маскировки. Клиент шлёт TLS ClientHello с реальным SNI (например `apple.com`), внутри — зашифрованный VLESS. DPI обычно смотрит на:
1. **Destination IP** — наш зарубежный exit. Если IP в реестре — блок.
2. **SNI в ClientHello** — `apple.com`. Если SNI **не в whitelist'е региона** — блок.
3. **TLS fingerprint** — у нас `chrome`, не палится.

В обычных регионах (1) и (2) проходят: IP не в реестре, SNI = `apple.com` пропускается DPI как обычный HTTPS на Apple. В регионах с **whitelist-режимом** проходит только (3) — IP+SNI режутся.

**Решение:** SNI = РУ-домен из whitelist'а оператора (`ads.x5.ru`, `dzen.ru`, `ya.ru`). На сервере Reality `dest` указывает на тот же РУ-домен — при non-VPN active probing'е сервер форвардит handshake к реальному `ads.x5.ru:443`, отдаёт его сертификат → DPI видит «как будто юзер ходит на Пятёрочку», пропускает.

**С точки зрения каждого участника:**
- **Юзер**: коннектится на наш сервер, в ClientHello SNI=`ads.x5.ru`. С виду — стандартный HTTPS на российский ритейл-сайт.
- **DPI оператора (Хакасия LTE)**: видит SNI=`ads.x5.ru` — РУ-домен, пропускает. Не делает probing (или если делает — получает валидный сертификат от реального ads.x5.ru через passthrough).
- **Наш Xray на :1443**: расшифровывает Reality, выпускает в интернет через `freedom`.
- **РУ-сервисы юзера**: routing-правила (см. ниже) шлют их **напрямую через ISP** (split-tunnel). Через VPN идёт только зарубежный трафик.

**Главное: наш `dest` должен быть достижим с exit'а наружу**. Иначе active probing'и DPI спалят сервер. См. [подбор dest](#-подбор-dest--serverNames).

---

## 🏗 Архитектура

```
┌───────────────────────────────────────────────────────────────────┐
│  📱 Happ                                                           │
│                                                                    │
│  Конфиг (JSON-формат подписки):                                    │
│    address: 178.105.1.202, port: 1443                              │
│    serverName: ads.x5.ru,  publicKey: GTmCq-rBPv...                │
│                                                                    │
│  routing.rules:  RU-домены/IP → direct (через ISP юзера)           │
│                  Apple/iCloud  → direct                            │
│                  bittorrent    → direct                            │
│                  всё остальное → proxy (через VPN)                 │
└──────────────────────┬────────────────────────────────────────────┘
                       │ TCP :1443
                       │ TLS ClientHello SNI=ads.x5.ru
                       │ (DPI: «РУ-домен в whitelist'е, пропускаю»)
                       ▼
┌───────────────────────────────────────────────────────────────────┐
│  🇩🇪 Exit VPS 178.105.1.202 (Hetzner Falkenstein)                  │
│                                                                    │
│  Xray inbounds:                                                    │
│    - vless-reality-rusni-in :1443  serverNames=[ads.x5.ru]         │
│      dest=ads.x5.ru:443  ← passthrough при probing'е              │
│    - vless-reality-in :8443        serverNames=[apple.com]         │
│      dest=apple.com:443                                            │
│    - api :10085  (gRPC, доступен только backend'у через iptables) │
│                                                                    │
│  Расшифровывает Reality, freedom outbound в интернет.              │
└───────────────────────────────────────────────────────────────────┘
```

**Один и тот же VPS обслуживает оба inbound'а** — экономия ресурсов и упрощение управления.

---

## 📦 Что в `vpn_servers`

LTE-инбаунд — это **отдельная строка** в `vpn_servers` с тем же `xray_api_host`, что у обычного inbound'а на этом же VPS, но другими `port`/`server_names`/`dest`/`inbound_tag`/`public_key`/`short_id`:

| поле | обычный inbound (apple.com) | LTE inbound (ads.x5.ru) |
|---|---|---|
| `name` | `Germany-02` | `[LTE 1] Мобильный интернет` |
| `country_code` | `DE` | `DE` (физика exit'а та же) |
| `host` | `178.105.1.202` | `178.105.1.202` (тот же VPS) |
| `port` | `8443` | **`1443`** |
| `server_names` | `apple.com` | **`ads.x5.ru`** |
| `dest` | `apple.com:443` | **`ads.x5.ru:443`** |
| `public_key` / `private_key` / `short_id` | пара #1 | **пара #2** (генерируется отдельно) |
| `inbound_tag` | `vless-reality-in` | **`vless-reality-rusni-in`** |
| `xray_api_host` | `178.105.1.202` | `178.105.1.202` (тот же) |
| `xray_api_port` | `10085` | `10085` (тот же) |
| `priority` | `0` | **`>0`** (например `10` — попадает в priority-блок подписки) |

**Почему это работает без правок Go-кода**: бэкенд в `xray.Pool` подключается к одному gRPC-API (`xray_api_host:xray_api_port`), но `AddUser`/`RemoveUser` гоняет с разными `inbound_tag`. ResyncServer на новый `vpn_servers.id` заведёт всех существующих юзеров через AddUser с новым тегом → они попадут в LTE-инбаунд автоматически.

В подписке оба inbound'а становятся **отдельными ссылками**:
- `🇩🇪 Germany-02` (port 8443, обычный) — для тех, у кого работает прямой apple.com SNI
- `🇩🇪 [LTE 1] Мобильный интернет` (port 1443, RU-SNI) — для проблемных регионов

`priority>0` дополнительно поднимает LTE-запись **сразу под первый ⚡ режим** — чтобы юзер из проблемной сети не листал весь список географий. См. <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/docs/SUBSCRIPTION.md" />.

---

## 🚀 Как добавить LTE-инбаунд на существующий VPS

> Эта инструкция для случая «у нас уже есть exit-VPS с обычным `:8443` apple.com inbound'ом, добавляем второй inbound на :1443 с RU-SNI». Если разворачиваешь VPS с нуля — сначала <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/add_server/README.md" /> (обычный exit), потом эта инструкция.

### 1. Подобрать `dest` / `serverNames` (РУ-домен)

См. [секцию ниже](#-подбор-dest--serverNames). Обязательно проверь, что домен достижим **с exit'а наружу**:

```bash
ssh root@<EXIT_IP> 'curl -sI --max-time 5 https://<RU_DOMAIN>/ | head -1'
# хотим увидеть HTTP/200 или /301 или /302 — что-то осмысленное
```

Если geoblock'нул из EU — выбери другой РУ-домен.

### 2. Сгенерировать новые Reality-ключи

На самом VPS (или локально с Docker):

```bash
docker run --rm ghcr.io/xtls/xray-core:latest x25519
# PrivateKey: AJJNRbL4...
# Password (PublicKey): GTmCq-rBPv...
openssl rand -hex 8  # ShortID, например b470aa0f3b156a0f
```

Сохрани в безопасном месте (passwords manager, memory).

### 3. Дополнить `/opt/xray/config.json` вторым inbound'ом

К существующему массиву `inbounds` добавить:

```json
{
  "tag": "vless-reality-rusni-in",
  "listen": "0.0.0.0",
  "port": 1443,
  "protocol": "vless",
  "settings": {"clients": [], "decryption": "none"},
  "streamSettings": {
    "network": "tcp",
    "security": "reality",
    "realitySettings": {
      "show": false,
      "dest": "ads.x5.ru:443",
      "xver": 0,
      "serverNames": ["ads.x5.ru"],
      "privateKey": "<NEW_PRIVATE_KEY>",
      "shortIds": ["<NEW_SHORT_ID>"]
    }
  },
  "sniffing": {"enabled": true, "destOverride": ["http","tls","quic"]}
}
```

`clients: []` — пустой; `AddUser` через ResyncServer заведёт UUID'ы.

### 4. Перезапустить Xray

```bash
ssh root@<EXIT_IP> '
  docker restart xray
  sleep 3
  docker ps --format "{{.Names}}\t{{.Status}}\t{{.Ports}}"
  docker logs xray 2>&1 | tail -10
'
```

Проверь, что в логах появилось `listening TCP on 0.0.0.0:1443`. **Существующие коннекты на :8443 разорвутся на 1-2 секунды** (юзеры авто-переподключатся, Happ обрабатывает это прозрачно). Это единственный момент простоя.

### 5. INSERT в `vpn_servers` (новая запись)

```sql
INSERT INTO vpn_servers (
    name, location, country_code, host, port,
    public_key, private_key, short_id, dest, server_names,
    xray_api_host, xray_api_port, inbound_tag, is_active,
    server_max_connections, description, priority
) VALUES (
    '[LTE 1] Мобильный интернет', '<Location> (RU-SNI обход)', 'XX',
    '<EXIT_IP>', 1443,
    '<NEW_PUBLIC_KEY>', '<NEW_PRIVATE_KEY>', '<NEW_SHORT_ID>',
    'ads.x5.ru:443', 'ads.x5.ru',
    '<EXIT_IP>', 10085, 'vless-reality-rusni-in', true,
    1000,
    'RU-SNI inbound для регионов с белым списком DPI (Хакасия и т.п.)',
    10  -- priority: попадает в priority-блок подписки, выше географий
)
RETURNING id;
```

### 6. ResyncServer на новый `id`

```bash
docker run --rm --network vpn-stack_vpn fullstorydev/grpcurl:latest \
    -plaintext -d '{"server_id": <NEW_ID>}' \
    vpn-core:50062 vpn.v1.VPNService/ResyncServer
# {"usersTotal": N, "usersAdded": N}
```

### 7. Открыть :1443 в iptables (если firewall закрыт)

`prepare-vps.sh` оставляет INPUT policy ACCEPT и закрывает только :10085. Поэтому для базовой настройки ничего не надо. Но если у тебя ужесточённый firewall:

```bash
iptables -I DOCKER-USER -p tcp --dport 1443 -j ACCEPT
ip6tables -I DOCKER-USER -p tcp --dport 1443 -j ACCEPT
netfilter-persistent save
```

### 8. Проверки

```bash
# TCP probe :1443 снаружи
timeout 3 bash -c '</dev/tcp/<EXIT_IP>/1443' && echo OK

# Подписка отдаёт новую строку
SUB_TOKEN=$(docker exec vpn-postgres psql -U vpn -d vpn -At -c \
    "SELECT subscription_token FROM vpn_users LIMIT 1;")
curl -s "https://cdn.osmonai.com/api/v1/subscription/${SUB_TOKEN}" \
    | base64 -d | grep -i "LTE\|x5"
# Должна быть VLESS-ссылка с port=1443 sni=ads.x5.ru

# JSON-формат содержит RU-domain rules в routing
curl -s "https://cdn.osmonai.com/api/v1/subscription/${SUB_TOKEN}?format=json" \
    | python3 -c "import sys,json; [print(c['remarks']) for c in json.load(sys.stdin) if 'LTE' in c.get('remarks','')]"
```

---

## 🧪 Как протестировать LTE-инбаунд вручную

Без Happ, прямо с любой машины с Docker:

```bash
cat > /tmp/lte-client.json <<EOF
{
  "log": {"loglevel": "warning"},
  "inbounds": [{
    "tag": "socks-in",
    "listen": "0.0.0.0",
    "port": 10808,
    "protocol": "socks",
    "settings": {"udp": true, "auth": "noauth"}
  }],
  "outbounds": [{
    "tag": "proxy",
    "protocol": "vless",
    "settings": {
      "vnext": [{
        "address": "<EXIT_IP>",
        "port": 1443,
        "users": [{
          "id": "<UUID-из-vpn_users>",
          "encryption": "none",
          "flow": "xtls-rprx-vision"
        }]
      }]
    },
    "streamSettings": {
      "network": "tcp",
      "security": "reality",
      "realitySettings": {
        "fingerprint": "chrome",
        "publicKey": "<NEW_PUBLIC_KEY>",
        "serverName": "ads.x5.ru",
        "shortId": "<NEW_SHORT_ID>"
      }
    }
  }]
}
EOF

docker run -d --name xray-lte-test \
    -v /tmp/lte-client.json:/etc/xray/config.json:ro \
    -p 127.0.0.1:11808:10808 \
    ghcr.io/xtls/xray-core:latest -c /etc/xray/config.json
sleep 3
curl --proxy socks5h://127.0.0.1:11808 https://ifconfig.me
# Ожидаемый ответ: IP exit-узла (178.105.1.202).
docker rm -f xray-lte-test
```

**Active probing симуляция** (что увидит DPI оператора, если решит проверить твой сервер):

```bash
# С любой машины — притвориться обычным TLS-клиентом, который ходит на ads.x5.ru
curl -sI --resolve ads.x5.ru:443:<EXIT_IP> https://ads.x5.ru/ | head -3
# Должен прийти валидный сертификат ads.x5.ru (через passthrough к реальному сайту)
# и реальный HTTP-ответ от ads.x5.ru — для DPI неотличимо от настоящего сайта.
```

Если `dest` мёртвый — ответа не будет / истечёт timeout, маскировка снижается.

---

## 🎯 Подбор `dest` / `serverNames`

Ключевые свойства хорошего РУ-домена для LTE-обхода:

| Свойство | Зачем | Как проверить |
|---|---|---|
| Реально работающий HTTPS | Reality пропускает probing'и через настоящий сайт | `curl -sI https://<DOMAIN>/` отдаёт 200/301/302 |
| Достижим из EU (наш exit) | без этого probing-passthrough не работает | `ssh root@<EXIT> 'curl -sI https://<DOMAIN>/'` — не должно быть geoblock'а |
| В whitelist'е оператора региона | без этого SNI режется DPI | сложно проверить заранее, проверяется живым тестом юзером в проблемной сети |
| Стабильный (не меняет CDN/IP часто) | иначе Reality может разъехаться с реальным сертификатом | sites крупных компаний (Сбер, Яндекс, X5, Mail.ru) обычно ок |
| Не палится антифродом за «странную активность» | если probing'и часто бьют — могут забанить наш exit | использовать поддомены (например `ads.x5.ru`, не `5ka.ru`) — там аналитика, нагрузка ожидаема |

### Кандидаты, проверенные на 2026-05-07

| Домен | Что | Достижим из EU | Используем |
|---|---|---|---|
| `ads.x5.ru` | X5 Retail Group реклама | ✅ HTTP/2 302 | ✅ id=144 |
| `dzen.ru` | Yandex Zen | ✅ обычно ок | резерв |
| `ya.ru` | Yandex main | ✅ обычно ок | резерв |
| `cloudtips.ru` | Тинькофф Tips | ✅ обычно ок | резерв |
| `gosuslugi.ru` | ❌ **НЕ использовать** — гос-домен, есть риск претензий | — | — |
| `sberbank.ru` | ❌ **НЕ использовать** — банк, тщательно мониторят probing'и | — | — |

> **Почему не использовать gosuslugi/банки**: их security-отделы могут заметить аномальную активность (чужие сертификат-fetch'и, странные User-Agent через probing) и эскалировать до провайдера. Лучше — нейтральные ритейл/медиа домены.

### Запасных в горячем резерве — 2-3

Если `ads.x5.ru` начнёт глючить (RKN добавит в реестр / X5 ужесточит rate-limit на TLS на этом поддомене / IP сменится) — нужно быстро переключиться. Держи 2-3 запасных:

```sql
-- Запасной LTE-инбаунд (создаётся аналогично, на том же VPS, на другом порту)
INSERT INTO vpn_servers (..., port=2443, server_names='dzen.ru', dest='dzen.ru:443',
                         inbound_tag='vless-reality-rusni-in-2', priority=11, is_active=false)
-- is_active=false: видим в БД, но в подписке не светим. Включаем когда LTE 1 умрёт.
```

---

## ⚠️ Подводные камни

1. **`dest` должен быть **достижим с exit-VPS**.** Иначе active probing спалит маскировку. См. [подбор](#-подбор-dest--serverNames).

2. **`shortId` НЕ пустой у нас**. У конкурента в realitySettings нет `shortId` — потому что на их сервере `shortIds: [""]` (пустая строка разрешена). У нас `shortIds: ["b470aa0f3b156a0f"]` — клиент **обязан** передавать именно этот shortId, иначе хендшейк отвалится. Если хочется как у конкурента — пересоздать inbound с `shortIds: [""]`. Смысла мало, наш shortId не палится.

3. **Routing-профиль для LTE — `profileBypass`, не `profileFull`** (RU direct, остальное proxy). Установлено в `writeJSONFormat` <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/services/gateway/internal/handler/subscription_config.go" />. Если эмитить LTE с profileFull — РУ-сервисы (банки, Госуслуги) ломаются (антифрод geo-блок).

4. **Один Xray, два inbound'а — оба `clients` синхронны** через ResyncServer. Если забыть `ResyncServer` после INSERT'а — новый inbound будет пустой и юзеры получат «no user matches the UUID» при коннекте. Всегда после INSERT → ResyncServer.

5. **Перезапуск Xray рвёт текущие коннекты** на 1-2 секунды. Происходит при добавлении нового inbound'а. Делай перезапуск **в тихие часы** (3-5 утра МСК), а не в пиковую нагрузку (~21:00 МСК).

6. **DNS-блокировки в проблемных регионах** ✅ **решено для LTE** (2026-05-07).

   У нас `buildDNS()` использует **DoH** (`https://cloudflare-dns.com/dns-query`). В Хакасии/проблемных регионах могут резать DoH-эндпоинты, потому что `cloudflare-dns.com` тоже не в whitelist'е оператора → DNS не резолвится → юзер видит «no internet» при включённом VPN.

   **Фикс**: для LTE-серверов (priority>0 + RU-TLD в `server_names`) handler `writeJSONFormat` подставляет `buildPlainDNS()` вместо `buildDNS()`. Это **plain UDP DNS** на `1.1.1.1`/`1.0.0.1` без TLS — DPI не может проверить SNI и пропускает.

   Каскадные priority-серверы (`sni=apple.com`) сохраняют DoH — их DNS обычно не нужен в проблемных регионах, у них и так apple.com SNI который пропускают любые DPI.

   Детектор: `isRussianSNI(srv.GetServerNames())` — `.ru` / `.рф` / `.su` / `.xn--p1ai`. См. <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/services/gateway/internal/handler/subscription_config.go" />.

7. **`pickDefaultServer` пропускает priority>0**. То есть LTE-сервер **никогда не становится дефолтом для режимов** (⚡/🚀/🎬). Это by design: LTE — точечная опция, не «универсальный VPN». Если переименовать LTE в дефолтную страну — режимы переедут на другой normal-сервер.

---

## 📊 Мониторинг adoption

Для priority-серверов (LTE, каскад) ожидаемый трафик низкий — это «опции для проблемных регионов», не основная нагрузка. Метрика «работает ли LTE-обход» — **есть ли вообще не-нулевой трафик** у юзеров не из тестовых аккаунтов.

```bash
# 24h adoption по LTE и каскаду
docker exec vpn-postgres psql -U vpn -d vpn -c "
  SELECT s.id, s.name,
    COUNT(DISTINCT vpn_user_id) FILTER (WHERE collected_at >= NOW() - INTERVAL '1 hour')  AS u_1h,
    COUNT(DISTINCT vpn_user_id) FILTER (WHERE collected_at >= NOW() - INTERVAL '24 hours') AS u_24h,
    pg_size_pretty(SUM(uplink_bytes+downlink_bytes) FILTER (WHERE collected_at >= NOW() - INTERVAL '24 hours')) AS bytes_24h
  FROM vpn_servers s LEFT JOIN traffic_samples t ON t.server_id = s.id
  WHERE s.priority > 0 GROUP BY s.id, s.name ORDER BY s.priority;
"
```

Готовый watcher-скрипт для каскада: <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/deploy/scripts/cascade-watch.sh" />. Аналогичный нужно завести для LTE-инбаундов (TODO).

---

## 🗂️ Текущий деплой (на 2026-05-07)

| ID | name | host:port | server_names | inbound_tag | priority |
|---|---|---|---|---|---|
| 143 | `Germany-02` | `178.105.1.202:8443` | `apple.com` | `vless-reality-in` | `0` |
| **144** | **`[LTE 1] Мобильный интернет`** | **`178.105.1.202:1443`** | **`ads.x5.ru`** | **`vless-reality-rusni-in`** | **`10`** |

`xray_api_host=178.105.1.202`, `xray_api_port=10085` для обоих — один Xray, два inbound'а, один gRPC API.

Reality-ключи **разные** для двух inbound'ов (генерируются отдельно через `x25519`).

---

## 📋 TODO

- [x] **Plain DNS для LTE-инбаундов** ✅ 2026-05-07 — `isRussianSNI` детектор + `buildPlainDNS()` override в `writeJSONFormat`.
- [ ] **2-3 запасных LTE-инбаунда** в горячем резерве с разными `dest` (`dzen.ru`, `ya.ru`) — если основной spalится.
- [ ] **TCP-health-check для LTE :1443** в `vpn-core` (сейчас бэкенд пингует только `xray_api_host:xray_api_port = :10085`).
- [ ] **`prepare-vps-rusni.sh`**: bootstrap-скрипт «всё-в-одном» для нового VPS с двумя inbound'ами сразу.
- [ ] **`deploy/scripts/lte-watch.sh`** по аналогии с <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/deploy/scripts/cascade-watch.sh" />.

---

## 🔗 Связанные документы

- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/docs/vpn/cascade.md" /> — каскадная схема (relay через РФ), та же проблематика DPI-обхода, другой механизм.
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/docs/SUBSCRIPTION.md" /> — формат подписки, priority-блок, JSON vs base64.
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/add_server/README.md" /> — runbook для bootstrap'а exit-VPS с нуля.
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/services/gateway/internal/handler/subscription_config.go" /> — handler подписки, `splitByPriority`, `pickDefaultServer`, `writeJSONFormat`.
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/services/vpn-service/migrations/009_add_server_priority.up.sql" /> — миграция `priority` в `vpn_servers`.
