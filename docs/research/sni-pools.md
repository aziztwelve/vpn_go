# SNI pools per VPS — Reality donor candidates

**Дата:** 2026-05-11 (последнее обновление — добавлен `tw1` NSK)
**Статус:** 🟢 Stage 3 — разведка завершена для `fi02`, `ru01`, `tw1` (RU LTE node). `nl01` отложен.

## Методология

1. RealiTLScanner v0.x на каждой ноде, скан собственного `/24` с порта 443:
   ```bash
   ./RealiTLScanner-linux-64 -addr <subnet>/24 -port 443 -thread 30 -timeout 8 -out scan.csv
   ```
2. Фильтр сканера сразу даёт `TLS 1.3 + ALPN h2` only.
3. Дополнительно `curl --resolve <domain>:443:<IP>` для каждого кандидата —
   проверяем что прямой TLS handshake по IP отдаёт валидный HTTP-ответ.
4. Отбраковка вручную: VPN/gambling/dating/CDN-origin/fake-cert/Kubernetes-default.

## Критерии финального отбора

- ✅ TLS 1.3, ALPN h2, валидный CA-серт (Let's Encrypt / GlobalSign / etc — НЕ self-signed)
- ✅ Прямой `curl` по IP с `--resolve` отдаёт `2xx/3xx` (4xx допустимо для `serverNames` pool, но не для `dest`)
- ✅ Сайт не из категорий VPN/proxy/gambling/adult/dating/crypto-mining (палево)
- ✅ Не CDN-origin сертификат (`CloudFlare Origin Certificate`, `Fastly default` — handshake работает, но прямой контент не отдаётся → подозрительно при активном пробинге)
- ✅ Желательно разнообразие: разные категории (B2B SaaS, energy, hosting, mail, etc), разные org/регистраторы
- ✅ Для RU-нод (AS48282 vdsina и далее) — приоритет cover-stories из RU-сегмента (VK, Max, Yandex), они белые в RKN-feed и доверенные у мобильных операторов

## Reality dest vs serverNames

В Xray Reality:
- **`dest`** — primary. На этот IP:port идёт fallback при mismatched handshake. Должен отдавать **2xx/3xx** на `GET /` (иначе при активном пробинге DPI увидит «странный сайт»).
- **`serverNames[]`** — пул допустимых SNI. Клиент рандомит выбор. Для каждого SNI на IP `dest`-а должен быть **валидный сертификат** (что и подтвердил наш scan).
- 4 элемента в `serverNames` per inbound — баланс между разнообразием (разные SNI от запроса к запросу — антипаттерн-фильтр для сетевого ML) и риском что один из них кэшируется/блочится.

---

## fi02 — `204.168.248.33` (NL, /24 = 204.168.248.0/24)

**Контекст:** non-RU зарубежная нода. Категория DPI на пути сюда не критична; основная цель — чтобы сертификат на SNI совпадал с IP, и handshake выглядел нормально. Текущий `dest=apple.com` — палевно для повторных коннектов на `204.168.248.x`.

### Скан

22 кандидата с TLS 1.3 + h2. Лог: `/tmp/sni-scan/fi02-scan.csv`.

### Отбракованы

| SNI | Причина |
|---|---|
| `support.yay88win.com` | gambling/casino domain |
| `demo.betengines.com` | betting platform |
| `fi1.lumiovpn.com` | другой VPN-провайдер (палево!) |
| `www.loveis.com.br` | dating |
| `Kubernetes Ingress Controller Fake` | default fake cert |
| `RunCloud Web Certificate` | platform default cert |
| `CloudFlare Origin Certificate` (×3) | CDN-origin, прямой `GET /` не отдаёт |
| `proquelec.wanekoohost.com` | 302 redirect, ненадёжно |
| `api.klearbox.net`, `*.h3s.dk` | 404 на `/` (для `serverNames` ОК, но мы выбрали 200-only) |
| `rcmotos.es` | timeout при curl |

### ✅ Отобранный пул (4 SNI)

| Роль | SNI | IP | HTTP | Cert | Категория |
|---|---|---|---|---|---|
| **dest** (primary) | `creative-demo.dh.sg` | `204.168.248.87` | 200 OK + HTML | Let's Encrypt | Singapore agency / business |
| serverName | `crm.legalexito.com` | `204.168.248.92` | 200 OK + HTML | Let's Encrypt | Legal SaaS / CRM |
| serverName | `ekizenergy.tech` | `204.168.248.156` | 200 OK + HTML | Let's Encrypt | Energy / .tech TLD |
| serverName | `mail.mxhosting.org` | `204.168.248.2` | 200 OK + HTML | Let's Encrypt | Email hosting |

**Резерв (если что-то отвалится):**
- `fixaki.com.cy` (`204.168.248.155`) — Cyprus business, 200 OK
- `testi.surma-aho.com` (`204.168.248.162`) — Finnish, 200 OK
- `play-herd.com` (`204.168.248.41`) — gaming, 200 OK
- `cp.croflow.in` (`204.168.248.249`) — admin/control panel, 200 OK

### Конфиг для fi02

```json
"realitySettings": {
  "show": false,
  "dest": "creative-demo.dh.sg:443",
  "xver": 0,
  "serverNames": [
    "creative-demo.dh.sg",
    "crm.legalexito.com",
    "ekizenergy.tech",
    "mail.mxhosting.org"
  ]
}
```

---

## ru01 — `91.184.245.196` (RU, AS48282 VDSINA, /24 = 91.184.245.0/24)

**Контекст:** RU-нода. Сейчас работает как `dokodemo-door` relay → `fi02:8443`, не как Xray Reality. Но **SNI на пути юзер→ru01 всё равно виден RU-DPI** — и для оператора Хакасии важно увидеть на этом пути «правильный» SNI вида VK/Max/Yandex, иначе keyword-фильтр срабатывает (см. `17-ru-vps-lte.md`).

При миграции ru01 в Reality (или при апгрейде MWS-ноды в Stage 4) — этот пул применяется напрямую.

### Скан

24 кандидата. Лог: `/tmp/sni-scan/ru01-scan.csv`.

### Отбракованы

| SNI | Причина |
|---|---|
| `admin.vpn-for-friends.com` | VPN-related (палево!) |
| `*.local` (cert-issuer=`OpenClaw`) | соседний наш сервер 🙃 — self-signed, skip |
| `myserver` (`inter`) | self-signed default |
| `images.apple.com` (на vdsina IP) | fake/proxied — серт от Apple, но IP = vdsina, активный пробер сразу спалит несоответствие |
| `yahoo.com` (на vdsina IP) | то же |
| `github.com` (на vdsina IP) | то же — суспициозно |
| `j.sni-644-default.ssl.fastly.net` | Fastly default cert |
| `v2907867.hosted-by-vdsina.ru`, `v2714914.hosted-by-vdsina.ru` | generic vdsina default reverse-DNS, скорее всего пустые сервера |
| `xn-----2026-...xn--p1ai` | punycode RU-домен с цифрой 2026 — выглядит как фишинг/scam |

### ✅ Отобранный пул (4 SNI)

| Роль | SNI | IP | HTTP | Cert | Категория |
|---|---|---|---|---|---|
| **dest** (primary) | `grishchenkov.ru` | `91.184.245.31` | 200 OK + HTML | Let's Encrypt | Personal RU |
| serverName | `m.vk.com` | `91.184.245.34` | 418 (VK anti-scrape) | GlobalSign `*.vk.com` | **VK мобайл** — главное прикрытие в RU-DPI |
| serverName | `www.max.ru` | `91.184.245.46` | 301 redirect | GlobalSign `*.max.ru` | **Max мессенджер** (VK Holding) — белый у RKN |
| serverName | `mail.hohlov.tech` | `91.184.245.198` | 200 OK | Let's Encrypt | Personal mail / .tech TLD |

**Замечания по выбору:**
- `*.vk.com` и `*.max.ru` — **главная ценность** этого пула. VK и Max — белые в RKN/мобильных операторах, никогда не блочатся. SNI=vk.com → DPI «доверяет» и не делает active probing.
- `grishchenkov.ru` как `dest` — единственный кандидат с 200+HTML на `/` среди RU-сегмента (важно для fallback при mismatched handshake).
- `m.vk.com` отдаёт 418 — для `serverNames` это OK (TLS handshake валидный, cert валидный, ALPN h2). Но как `dest` — нет.

**Резерв:**
- `*.yandex.tr` (`91.184.245.60`) — Yandex Turkey. GlobalSign cert. 406 на `/`. OK для пула serverNames.
- `dion.vc` (`91.184.245.117`) — Dion company, 200 OK
- `m.devrandom-as-a-service.com` (`91.184.245.13`) — нейтральный tech-домен, 200

### Конфиг для ru01 (если будем переводить в Reality)

```json
"realitySettings": {
  "show": false,
  "dest": "grishchenkov.ru:443",
  "xver": 0,
  "serverNames": [
    "grishchenkov.ru",
    "m.vk.com",
    "www.max.ru",
    "mail.hohlov.tech"
  ]
}
```

---

## tw1 — `186.246.31.92` (RU, TimeWeb Cloud NSK, AS9123, /24 = 186.246.31.0/24)

**Контекст:** RU LTE-нода (Stage 4 в `end_sni.md`). TimeWeb NSK — Новосибирск, RU resident IP, ASN AS9123. Цель — обход keyword-DPI у мобильных операторов (МТС/Мегафон, Хакасия и др.) через SNI прикрытие *.vk.com / *.max.ru / *.music.yandex.ru.

### Скан

23 кандидата с TLS 1.3 + h2 в /24, за 14 сек. Лог: `sni-scan-raw/tw1-nsk-2026-05-11.csv`.

### Отбракованы

| SNI | Причина |
|---|---|
| `nsk-1-vm-*.twc1.net` (×4) | Hestia Control Panel default certs — пустые TimeWeb VPS |
| `images.apple.com` (×2, Apple Inc. cert) | подделка/проксирование на TimeWeb IP — палево при active probing |
| `github.com` (Sectigo cert) | НЕ настоящий GitHub (он на DigiCert), self-hosted git с похожим SNI |
| `dev-api.namelessvpn.net` | конкурент-VPN — категорически палево |
| `*.yandex.tr` | Yandex Turkey — `.tr` не RU, теряем главный плюс (RU camouflage) |
| `sub.omi-home.online`, `bot-rn.space` | подозрительные TLD/новорегистрации |
| `mbspl.as`, `admin.k0sha.su` | непонятные домены, не выглядят как обычный SMB |

### ✅ Отобранный пул (4 SNI)

| Роль | SNI | IP | HTTP | Cert | Категория |
|---|---|---|---|---|---|
| **dest** (primary) | `www.ub4hav.ru` | `186.246.31.164` | 200 OK + HTML | **GlobalSign** | RU SMB / corporate |
| serverName | `m.vk.com` | `186.246.31.102` | 200 (HEAD=418) | GlobalSign `*.vk.com` | **VK мобайл** — главное прикрытие |
| serverName | `web.max.ru` | `186.246.31.252` | 200 OK | GlobalSign `*.max.ru` | **Max мессенджер** (VK Holding) — белый у RKN |
| serverName | `music.yandex.ru` | `186.246.31.249` | 200 OK | GlobalSign `*.music.yandex.ru` | **Яндекс.Музыка** — белый у всех мобильных операторов |

**Замечания по выбору:**

- 🎯 **Главный выигрыш:** все 4 SNI имеют **GlobalSign** chain. Когда Reality генерирует fake-cert по запрошенному SNI, issuer-chain копируется с `dest` — а dest тоже GlobalSign → issuer консистентен с VK/Max/Yandex. Активный пробер видит `*.vk.com / GlobalSign` или `*.max.ru / GlobalSign` — что и должно быть в норме.
- Лучше чем ru01 (где `grishchenkov.ru` Let's Encrypt — issuer mismatch с GlobalSign-VK при cross-validation).
- `ads.x5.ru` (302) рассматривался как dest, но 302 для primary fallback менее устойчив чем 200 на `www.ub4hav.ru`. Оставлен в резерве.
- `m.vk.com` отдаёт 418 на HEAD (VK anti-scrape), но GET=200 — для serverNames это полностью OK.
- Все 4 IP в `186.246.31.0/24` — одна подсеть, один TimeWeb NSK pool → reverse-DNS не выпадает.

**Резерв:**
- `dipnova.ru` (`186.246.31.116`) — Let's Encrypt, 200 OK, h2
- `excldlc.ru` (`186.246.31.29`) — Let's Encrypt, 200 OK, h2
- `www.moneouniform.ru` (`186.246.31.214`) — **GlobalSign**, 200 OK, h2 (можно подменить как dest)
- `ads.x5.ru` (`186.246.31.110`) — Let's Encrypt, 302, корпсайт X5

### Конфиг для tw1

```json
"realitySettings": {
  "show": false,
  "dest": "www.ub4hav.ru:443",
  "xver": 0,
  "serverNames": [
    "www.ub4hav.ru",
    "m.vk.com",
    "web.max.ru",
    "music.yandex.ru"
  ]
}
```

---

## nl01 — `146.103.112.91` — ❌ unreachable

Port 22 timeout с этой машины. Возможные причины:
- VPS down
- Firewall блочит входящие SSH с IP openclaw-workspace
- Изменён IP / переехал

**TODO для Stage 4:** уточнить статус с aziz, повторно сканить когда будет доступ.

---

## MWS RU-VPS — ⏸ ждём Stage 4

Когда поднимем MWS-ноду (Stage 4 в `end_sni.md`):
1. Запустить тот же `RealiTLScanner -addr <MWS-subnet>/24 -port 443`
2. Особый интерес — есть ли в подсети MWS `ads.x5.ru` или другой `*.x5.ru` (использовался конкурентом geodataload.com — проверен на проход через РКН/мобильные DPI)
3. Если `ads.x5.ru` есть → ставить как `dest` (proven). Если нет → берём верхний 200-OK кандидат из скана.
4. 3 остальных SNI в `serverNames` — из скана, приоритет «корпсайты MWS / Magnit / X5» если попадутся.

---

## Acceptance (Stage 3 для существующих VPS)

- [x] RealiTLScanner запущен на доступных VPS (`fi02`, `ru01`, `tw1`)
- [x] CSV-логи получены и сохранены (`docs/research/sni-scan-raw/*.csv`)
- [x] Curl-валидация TLS handshake через `--resolve`
- [x] Подобраны 4 SNI per VPS с обоснованием
- [x] Записан этот файл (`docs/research/sni-pools.md`)
- [x] `tw1` — RU LTE-нода (TimeWeb NSK), пул сформирован (см. выше)
- [ ] `nl01` — **отложен** до восстановления доступа
- [ ] MWS-нода — деприоритезирована (взяли TimeWeb вместо MWS, см. `tw1` выше)
