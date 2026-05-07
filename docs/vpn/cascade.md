# Каскадная схема (relay через РФ)

Как работает обход РКН-блокировок Reality-протокола: юзер коннектится на VPS внутри РФ, который тупо переливает байты в зарубежный exit-узел.

**Статус реализации:** ✅ **в проде с 2026-05-07** (relay `91.184.245.196` → exit `204.168.248.33` Финляндия)

> Контекст: к весне 2026 РКН начал точечно резать Reality-хендшейк до зарубежных IP. Прямые ссылки на FI/NL стали нестабильно работать в части регионов РФ. Каскад через RU-VPS убирает зарубежный IP из сетевого стека юзера: первый коннект — внутри страны, дальше — TCP-relay.

---

## 🧠 Простыми словами

Reality — это TLS-протокол маскировки. Чтобы РКН не отличал VPN-трафик от обычного HTTPS, Xray-клиент шлёт SNI настоящего сайта (например `apple.com`), а внутри прячет VLESS-данные.

Проблема: РКН видит **destination IP**. Если IP зарубежный + Reality fingerprint — блок.

Решение: точка входа в РФ. Юзер шлёт хендшейк на `<RU_IP>:8443`, а специальный xray на этом VPS работает в режиме **dokodemo-door** (door = "дверь куда угодно") — побайтово форвардит TCP в exit-узел без расшифровки.

**С точки зрения каждого участника:**
- **Юзер**: коннектится внутрь РФ. Всё выглядит как HTTPS на `apple.com`.
- **РКН**: видит TCP-коннект до RU-IP. Reality-fingerprint есть, но destination — внутри страны, придраться не к чему.
- **Relay (RU)**: получает зашифрованный поток, не понимает payload, пересылает в exit. В логах — только source/dest IP.
- **Exit (FI)**: видит коннект с RU-IP, расшифровывает Reality, выпускает в интернет через `freedom`.
- **Бэкенд**: продолжает управлять exit-узлом напрямую (`AddUser`/`RemoveUser` через gRPC), relay не трогает.

**Главная фишка**: relay не знает про юзеров, ему нечего синхронизировать. Все UUID живут на exit, и при подключении юзера через relay они проедут до exit и там сматчатся.

---

## 🏗️ Архитектура

```
┌────────────────────────────────────────────────────────────────────┐
│  📱 Happ / V2RayN / Hiddify                                         │
│                                                                     │
│  Конфиг: vless://UUID@<RU_IP>:8443?pbk=...&sid=...&sni=apple.com   │
│  pbk/sid/sni — РЕАЛЬНЫЕ Reality-параметры exit-узла                │
└────────────────────┬───────────────────────────────────────────────┘
                     │ TCP :8443 (Reality cover SNI = apple.com)
                     ▼
┌────────────────────────────────────────────────────────────────────┐
│  🇷🇺 Relay VPS (например 91.184.245.196)                            │
│                                                                     │
│  Xray config:                                                       │
│    inbound[dokodemo-door]: listen :8443 → forward to <EXIT>:8443   │
│    outbound[freedom]: TCP raw                                       │
│                                                                     │
│  Не знает UUID. Не расшифровывает Reality. Просто переливает байты.│
│  Управления (gRPC API на :10085) НЕТ — этот VPS бэкенду не нужен.  │
└────────────────────┬───────────────────────────────────────────────┘
                     │ TCP :8443 raw
                     ▼
┌────────────────────────────────────────────────────────────────────┐
│  🇫🇮 Exit VPS (например 204.168.248.33)                             │
│                                                                     │
│  Xray config (стандартный, как у любого exit-узла):                │
│    inbound[vless-reality-in]: listen :8443, clients=[все UUID]     │
│    inbound[api]: listen :10085, dokodemo для gRPC HandlerService   │
│    outbound[freedom]: TCP/UDP в интернет                            │
│                                                                     │
│  Видит source = <RU_IP> вместо реального IP юзера.                  │
└────────────────────┬───────────────────────────────────────────────┘
                     │ gRPC AddUser/RemoveUser :10085
                     ▼
┌────────────────────────────────────────────────────────────────────┐
│  ⚙️ Backend (vpn-core, 178.104.217.201)                             │
│                                                                     │
│  Ходит НАПРЯМУЮ на <EXIT>:10085, а НЕ через relay.                  │
│  В vpn_servers для каскадной записи:                                │
│    host           = <RU_IP>      ← куда шлёт юзер                   │
│    xray_api_host  = <EXIT_IP>    ← куда ходит бэкенд                │
└────────────────────────────────────────────────────────────────────┘
```

---

## 📦 Что в `vpn_servers`

Каскад — это **отдельная строка** в `vpn_servers`, которая копирует Reality-параметры exit-узла, но указывает другой `host`:

| поле | значение | смысл |
|---|---|---|
| `name` | `<ExitName>-via-RU` | соглашение по нейминг'у |
| `country_code` | страна **exit** (FI/NL/DE) | флаг в Happ должен соответствовать реальному выходу |
| `host` | `<RU_IP>` | куда юзер шлёт Reality-хендшейк |
| `port` | `8443` | то же |
| `public_key` / `short_id` / `dest` / `server_names` | **те же что у exit** | потому что хендшейк завершится на exit |
| `xray_api_host` | `<EXIT_IP>` | бэкенд продолжает управлять exit-узлом |
| `xray_api_port` | `10085` | то же |
| `private_key` | приватник exit (формальная копия) | xray-сервер на relay его не использует |
| `is_active` | `true` | в подписке появится новой строкой |

**Почему это работает без правок Go-кода**: в `vpn_servers` поля `host` (куда коннектится клиент) и `xray_api_host` (куда ходит бэкенд для управления юзерами) **независимы**. Subscription handler читает `host` для генерации VLESS-ссылок, `xray.Pool` читает `xray_api_host`/`xray_api_port` для gRPC. Каскадная запись использует две разные точки.

Юзер в Happ при следующем `Profile-Update-Interval` (1 час) увидит **обе записи**:

```
🇫🇮 Finland           ← прямое подключение на exit, как было
🇫🇮 Finland-via-RU    ← новая каскадная ссылка через RU
```

Если каскад не работает — юзер переключится обратно на прямой одним тапом.

---

## 🚀 Как добавить новый relay

См. <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/add_server/README-relay.md" /> — пошаговый runbook для агента/человека. Кратко:

1. Получить SSH-доступ на RU-VPS, залить публичный ключ агента.
2. `ssh root@<RU_IP> 'bash -s' < add_server/prepare-relay-vps.sh` — поставит Docker, откроет `:22` и `:8443`, закроет всё остальное.
3. Сгенерировать конфиг с `dokodemo-door` → `<EXIT_HOST>:<EXIT_PORT>`, залить через scp, запустить контейнер `ghcr.io/xtls/xray-core:latest`.
4. INSERT в `vpn_servers` через `SELECT ... FROM vpn_servers WHERE id = <EXIT_ID>` — копирует Reality-параметры с exit-записи.
5. ResyncServer на новый ID — должно вернуть `usersAlready: N` (юзеры уже на exit, ничего не добавляется).
6. Проверка: подписка отдаёт обе записи + curl через каскад возвращает IP exit-узла.

**Скрипт bootstrap'а**: <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/add_server/prepare-relay-vps.sh" />

---

## 🧪 Как протестировать каскад вручную

Без Happ, прямо с любой машины с Docker:

```bash
cat > /tmp/client.json <<EOF
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
        "address": "<RU_IP>",
        "port": 8443,
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
        "publicKey": "<pbk-из-vpn_servers>",
        "serverName": "apple.com",
        "shortId": "<short_id-из-vpn_servers>"
      }
    }
  }]
}
EOF

docker run -d --name xray-test \
    -v /tmp/client.json:/etc/xray/config.json:ro \
    -p 127.0.0.1:11808:10808 \
    ghcr.io/xtls/xray-core:latest -c /etc/xray/config.json
sleep 3
curl --proxy socks5h://127.0.0.1:11808 https://ifconfig.me
# Ожидаемый ответ: IP EXIT-узла (например 204.168.248.33), НЕ <RU_IP>.
docker rm -f xray-test
```

Если ответ — IP exit-узла, каскад работает. Если timeout или connection refused — смотри логи на обоих узлах:

```bash
ssh root@<RU_IP>   'docker logs xray 2>&1 | tail -20'   # должны быть accepted tcp:<EXIT>:8443 [relay-in >> direct]
ssh root@<EXIT_IP> 'docker logs xray 2>&1 | tail -20'   # должны быть VLESS connections from <RU_IP>
```

---

## ⚠️ Подводные камни

1. **Если RU-IP попадёт в реестр РКН** — упадёт каскадная цепочка для всех юзеров на этой ссылке. Прямая ссылка на exit продолжит работать, юзер переключится в Happ одним тапом. Держи 2-3 RU-IP в горячем резерве в `vpn_servers` (можно `is_active=false` пока не нужны, переключать при инциденте).

2. **Latency** — большая отдельная тема, см. ниже [⏱️ Про задержку](#-про-задержку).

3. **Логи на relay**: `dokodemo-door` не пишет payload, но IP-ы клиентов видны при `loglevel: info`. Для prod-узлов используй `warning` (как делает `prepare-relay-vps.sh`).

4. **UDP не нужен**: Reality поверх TCP. В конфиге relay'а `network: tcp`. Если в будущем добавится QUIC-mode — нужно будет поднять отдельный `udp:443` inbound.

5. **TCP Fast Open / mptcp** — НЕ включай. `dokodemo-door` может глючить с этим.

6. **Whitelist `exit:8443` только для RU-IP** — паранойя на будущее. Скроет exit-IP от любых сканеров. **НЕ ДЕЛАЙ это пока в подписке висит и прямая ссылка на exit** — сломаешь её для тех юзеров, кто остался на прямом подключении. Когда уверен что все перешли на каскад — можно:
   ```bash
   iptables -I DOCKER-USER -p tcp --dport 8443 ! -s <RU_IP_LIST> -j DROP
   ```

7. **Проверка живости relay**: пока нет автоматического health-check специально для relay-узлов. `LoadCron` в `vpn-core` пингует `xray_api_host:xray_api_port` (то есть exit, а не relay). Если relay упадёт — `vpn-core` не узнает, юзеры просто получат timeout. **TODO**: добавить TCP-ping по `host:port` (`91.184.245.196:8443`) в health-check.

8. **Нагрузка**: relay просто переливает байты, latency-bottleneck — сеть. Один маленький VPS в РФ способен прокачать пиково 100-200Mbps. Если упрёшься — добавляй второй relay с тем же exit или каскадируй через CDN/Anycast.

---

## ⏱️ Про задержку

Каскад добавляет **один лишний хоп** между юзером и exit-узлом. Сколько именно RTT он стоит — зависит от того, где **физически** стоит relay-VPS, а не от того что написано в whois.

### Из чего складывается полное RTT в каскаде

```
[Юзер]  ──RTT₁──▶  [Relay]  ──RTT₂──▶  [Exit]  ──RTT₃──▶  [целевой сайт]
```

| Сегмент | Сколько обычно | Что влияет |
|---|---|---|
| RTT₁ (юзер↔relay) | 5-30ms если relay в РФ; 20-50ms если relay в EU с хорошим российским ASN | физическое расстояние, last-mile-провайдер юзера |
| RTT₂ (relay↔exit) | 1-3ms если в одном DC; 15-25ms если соседние EU-страны; 30-60ms через океан | где **на самом деле** стоят оба |
| RTT₃ (exit↔интернет) | то же что без каскада | география exit'а и таргета |

**Лишний overhead каскада ≈ RTT₁ + RTT₂ − (RTT прямой юзер→exit)**.

### ⚠️ "RU-IP" не равно "физически в России"

**Важнейший нюанс**, который мы поняли уже после деплоя `91.184.245.196`:

| Источник | Говорит | Реальность |
|---|---|---|
| `whois 91.184.245.196` | `country: RU`, AS48282 vdsina | юридически — да, RU |
| `ipinfo.io` | `city: Moscow` | догадка по AS, не физическое местоположение |
| `ping 8.8.8.8` | **0.3ms** | DC того же города/peering, что Google PoP — НЕ Москва |
| `ping 77.88.8.8` (Yandex Moscow) | **3.3ms** | физическое расстояние ~ Хельсинки↔Москва |
| `mtr 204.168.248.33` (FI exit) | через `dataix.eu` → `core32.hel1.hetzner.com` | выход явно через Helsinki |

То есть `91.184.245.196` — **российский IP, припаркованный к серверу в Helsinki**. Это объясняет почему RTT relay↔exit получился всего **17ms** (а не 30-60ms как было бы при честной Москве): они физически в одном/соседнем датацентре в Финляндии.

**Это палка о двух концах:**

✅ **Хорошо для latency**: относительно недорого — добавка ≈ 17ms на круг плюс TCP-handshake накладные расходы. На фоне "Москва ↔ Helsinki direct ≈ 25-40ms" — это **минимальная цена**, юзер заметит, но не катастрофически.

⚠️ **Не идеально для РКН-обхода**: каскад работает только если РКН блокирует на уровне **destination IP** (route-блок по reestr.gov.ru) или **TLS-fingerprint к зарубежным IP**. Если РКН-DPI смотрит **глубже** (например, считает packet size pattern или геолокацию следующего хопа), физическое нахождение сервера в Helsinki может проявиться. Реальный экзит через AS48282 (юридически RU) большинство DPI пройдут — это типовой кейс для "анти-DDoS" хостингов, которыми пользуются многие легальные RU-ресурсы.

### 📊 Реальные измерения (2026-05-07)

Конфигурация: relay `91.184.245.196` (vdsina, юр. RU / физ. HEL) → exit `204.168.248.33` (Hetzner HEL).

```
RU-relay  ↔  FI-exit:           ping avg 17.2ms    (10 packets, 0% loss, mdev 0.1ms)
RU-relay  →  Google 8.8.8.8:    ping avg 0.3ms     ← anycast, тот же DC
RU-relay  →  Yandex 77.88.8.8:  ping avg 3.3ms     ← MSK (физическое расстояние HEL→MSK)
RU-relay  →  *.hel1.hetzner:    1-2 hops          ← всё в одном датацентре
```

mtr показывает 8 хопов от relay до exit, все со стабильным RTT 15-17ms (нет джиттера, нет loss). Чистый путь.

### 🎯 Что заметит конкретный юзер

Допустим юзер из Москвы открывает Telegram через VPN.

| Сценарий | RTT юзер↔Telegram | Заметность |
|---|---|---|
| Без VPN (direct) | ~30-50ms (TG в EU) | baseline |
| Direct VPN: юзер→FI exit | RTT-юзер-до-FI + RTT-exit-до-TG ≈ **50-80ms** | +20-30ms vs baseline, ощутимо при загрузке чатов |
| Каскад: юзер→RU-relay→FI exit | RTT-юзер-до-relay + 17ms + RTT-exit-до-TG ≈ **65-100ms** | +15-20ms vs direct VPN |

**На что это влияет:**
- ✅ **Браузинг, мессенджеры, Spotify, YouTube** — почти неотличимо. Bandwidth тот же, +15-20ms на page load незаметны.
- ⚠️ **Видеозвонки (Zoom, Meet)** — заметно. Лип-синк может уплыть, эхо-cancellation становится сложнее.
- ❌ **Онлайн-игры (CS:GO, Valorant)** — критично. +15-20ms ping = разница между "комфортно" и "нокаут от teammates".
- ❌ **HFT/трейдинг** — никогда не используй каскад.

Именно поэтому каскад существует **в подписке параллельно** с прямой ссылкой, а не вместо. Юзер сам решает: «у меня direct не работает» (РКН режет) → переключаюсь на каскад. Или «работает обычная ссылка» — оставляю её.

### 💡 Как уменьшить latency каскада

1. **Выбирать relay-провайдера с good peering**. vdsina (наш кейс) физически сидит в HEL у Hetzner — это **идеально**: relay↔exit получается 17ms потому что они в одном DC. Если бы relay был у регионального провайдера в Воронеже с пирингом через MSK-IX → HEL, RTT₂ был бы 30-50ms.
2. **Геолокация relay должна быть между юзером и exit**. Москва-relay → FI-exit для питерского/московского юзера хорошо. Дальневосточный relay → FI-exit — плохо, юзер делает крюк через всю РФ.
3. **TCP_NODELAY на relay** включён в `dokodemo-door` по умолчанию — не отключай.
4. **MTU**: убедись что MTU между relay и exit ≥ 1480 (стандарт), чтобы не было фрагментации Reality-фреймов. Проверить: `ssh root@<RU_IP> 'tracepath 204.168.248.33 | grep mtu'`.
5. **Не использовать TCP Fast Open / Multipath TCP** — `dokodemo-door` ломается с этим.
6. **Bandwidth bottleneck = провайдер relay'а**. Один маленький VDS обычно прокачает 100-200Mbps пиково. Если упёрся — добавляй второй relay (горизонтальное масштабирование, см. [Multi-server architecture](../services/multi-server.md)).

### 🔬 Что стоит измерить перед деплоем нового relay

```bash
# 1. RTT relay ↔ exit (главный бюджет latency)
ssh root@<NEW_RU_IP> 'ping -c 20 -q <EXIT_IP>'

# 2. Где физически стоит сервер (whois врёт, ping 8.8.8.8 не врёт)
ssh root@<NEW_RU_IP> 'ping -c 5 8.8.8.8 && ping -c 5 77.88.8.8'
# 8.8.8.8 < 5ms И 77.88.8.8 > 5ms → сервер в EU
# 8.8.8.8 > 10ms И 77.88.8.8 < 5ms → сервер в РФ (как было заявлено)

# 3. Trace по пути до exit'а (нет ли странных хопов через США/Азию)
ssh root@<NEW_RU_IP> 'mtr -rwzbc 5 <EXIT_IP>'

# 4. Bandwidth между relay и exit (нужен iperf3)
ssh root@<EXIT_IP>   'iperf3 -s -1' &
ssh root@<NEW_RU_IP> 'iperf3 -c <EXIT_IP> -t 10'
```

Если RTT₂ > 30ms или bandwidth < 100Mbps — relay плохой, ищи другого провайдера.

---

## 🗂️ Текущий деплой (на 2026-05-07)

| ID | name | host (юзер шлёт сюда) | xray_api_host (бэкенд управляет тут) | exit-IP |
|---|---|---|---|---|
| 91 | Finland | `204.168.248.33` (Hetzner HEL) | `204.168.248.33` | свой |
| **142** | **Finland-via-RU** | `91.184.245.196` (vdsina, юр. RU / физ. HEL) | `204.168.248.33` (FI exit) | `204.168.248.33` |

Reality-параметры id=142 идентичны id=91 (`pbk=hLFCy5-0rDF1LDE9rK5EPOdV-tF4Upz77M1_Tyk4bCE`, `sid=b6d2c77a70827f02`, `sni=apple.com`).

**Замер latency**: `91.184.245.196 ↔ 204.168.248.33` ping avg `17.2ms` (mdev 0.1ms). Оба сервера физически в Helsinki, провайдеры разные (vdsina vs Hetzner) но соединены через `dataix.eu` peering. Подробнее — секция [⏱️ Про задержку](#-про-задержку).

---

## 🔗 Связанные документы

- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/docs/vpn/lte.md" /> — LTE-обход (RU-SNI inbound). Альтернативный механизм DPI-обхода для регионов с белым SNI-списком (Хакасия 2026). Каскад и LTE решают разные задачи и могут существовать параллельно.
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/add_server/README-relay.md" /> — runbook для добавления нового relay-узла.
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/add_server/prepare-relay-vps.sh" /> — bootstrap-скрипт для RU-relay.
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/add_server/README.md" /> — runbook для exit-узла (для сравнения).
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/docs/services/multi-server.md" /> — как `xray.Pool` управляет несколькими серверами.
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/docs/services/xray-integration.md" /> — gRPC API Xray, AddUser/RemoveUser.
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/services/gateway/internal/handler/subscription_config.go" /> — handler подписки: `splitByPriority`, `pickDefaultServer`, формирование base64/JSON.
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/services/vpn-service/migrations/009_add_server_priority.up.sql" /> — миграция `vpn_servers.priority` (каскадная запись id=142 идёт с `priority=20`, попадает в priority-блок подписки).
