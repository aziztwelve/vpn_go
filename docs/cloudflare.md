# Cloudflare — перенос домена на CF DNS

**Дата:** 2026-05-07
**Статус:** ✅ применено на `osmonai.com`
**Автор:** Devin + aziz

Гайд по подключению домена к Cloudflare как DNS-провайдеру (не transfer регистратора, а именно смена nameservers). Описывает что было сделано для `osmonai.com` и служит шаблоном для будущих доменов проекта.

---

## 🎯 Зачем CF

| Что даёт | Кому критично |
|---|---|
| Прячет реальный IP origin-VPS (`178.104.217.201`) от ботов и сканеров | gateway/cdn |
| Anti-DDoS на L3/L4 + L7 (бесплатно на Free plan) | gateway/cdn |
| Глобальный CDN-кеш для статики Next.js (`/_next/static/*`) | cdn (Mini App) |
| HTTP/3, TLS 1.3, Brotli, gzip автоматом | cdn |
| Автоматическая смена SSL-серта на edge (Universal SSL) | оба |

**Важно: НЕ ставим CF на VPN-ноды (Reality/Xray).** CF не понимает TLS-fingerprint Reality и не пробрасывает кастомные порты (8443) на Free-плане. VPN-ноды должны оставаться **DNS only ☁️** или вообще без записи в CF.

---

## 📋 Что было сделано для `osmonai.com`

### Финальная конфигурация DNS

| Запись | Тип | Content | Proxy | Назначение |
|---|---|---|---|---|
| `api` | A | `178.104.217.201` | 🟠 Proxied | Gateway HTTP API + Telegram webhook |
| `cdn` | A | `178.104.217.201` | 🟠 Proxied | Mini App (vpn_next, Next.js) |
| `connect` | A | `204.168.248.33` | ☁️ DNS only | legacy VPN-нода (Reality, не проксировать) |
| `de` | A | `204.168.248.33` | ☁️ DNS only | legacy VPN-нода |
| `osmonai.com` (apex) | A | `204.168.248.33` | ☁️ DNS only | legacy, не используется в коде |
| `www` | A | `204.168.248.33` | ☁️ DNS only | legacy, не используется в коде |
| `osmonai.com` | MX × 5 | `eforward[1-5].registrar-servers.com` | DNS only | Namecheap email forwarding |
| `osmonai.com` | TXT | `v=spf1 include:spf.efwd.registrar-servers.com ~all` | DNS only | SPF |

### Настройки зоны

- **SSL/TLS → Overview:** Full (strict)
- **SSL/TLS → Edge Certificates:** Always Use HTTPS = ON, Min TLS = 1.2, Automatic HTTPS Rewrites = ON
- **Security → Settings:** Browser Integrity Check = OFF
- **Security → Bots:** Bot Fight Mode = OFF
- **Plan:** Free (Pro не требуется)

### Регистратор

- `osmonai.com` зарегистрирован в **Namecheap**
- NS изменён с `dns1/dns2.registrar-servers.com` → `mitchell.ns.cloudflare.com` + `sara.ns.cloudflare.com`
- Email forwarding Namecheap (`eforward*.registrar-servers.com` MX) продолжает работать т.к. MX импортированы в CF в DNS only режиме

---

## 🚀 Пошаговый гайд для нового домена

Ниже — последовательность шагов. Используется как чек-лист при подключении любого нового домена проекта.

### Шаг 1. Добавить зону в Cloudflare

1. https://dash.cloudflare.com → **Add a domain** → ввести **только корень** (`example.com`, без `cdn.` / `api.`)
2. Выбрать план: **Free** (Pro не нужен — для VPN-проекта)
3. CF просканит текущие DNS-записи у регистратора и предложит импортировать
4. **Не нажимай Continue сразу** — проверь импорт на следующем шаге

### Шаг 2. Привести DNS в правильный вид (ДО переключения NS)

Пройдись по всем импортированным записям и расставь Proxy-флажок:

| Что за запись | Proxy флажок |
|---|---|
| `api`, `cdn` (web frontend / API за Caddy) | 🟠 **Proxied** — да, через CF |
| VPN-ноды (xray-серверы, Reality SNI, любые домены идущие на нестандартный порт типа 8443) | ☁️ **DNS only** — обязательно, иначе VPN сломается |
| `MX`, `TXT`, `SPF`, `DKIM`, `DMARC` (почта) | DNS only (CF их и не предлагает проксировать) |
| `_acme-challenge` (если есть от Caddy/certbot DNS-01) | DNS only |
| `apex` (`@`) и `www` | зависит от того что должно открываться (см. ниже) |

**Если apex/www должны открывать сайт:**
- Поменяй A-запись на IP origin (тот же что у `cdn`/`api`, например `178.104.217.201`)
- Включи 🟠 Proxied
- Добавь блок-редирект в Caddyfile (см. § Caddyfile ниже)

**Если apex/www не нужны** — оставь как есть в DNS only или удали записи.

### Шаг 3. Сразу настроить SSL/TLS (до переключения NS можно)

**SSL/TLS → Overview** → выбрать **Full (strict)**.

⚠️ **Сначала проверь что origin отдаёт валидный публичный серт:**

```bash
curl -vI https://api.example.com/health \
  --resolve api.example.com:443:<ORIGIN_IP> 2>&1 \
  | grep -E 'subject:|issuer:|expire'
```

Должно быть:
- `subject: CN=api.example.com`
- `issuer: ... O=Let's Encrypt ...` (или другой публичный CA)
- `expire date` — в будущем

Если `subject: CN=Caddy Local CA` — Caddy на staging-CA, выпустит невалидный серт, **Full (strict) сломает сайт**. Сначала переключи Caddy на prod LE (закомментируй staging-блок в `Caddyfile`), потом ставь strict.

**SSL/TLS → Edge Certificates:**
- Always Use HTTPS: **ON**
- Automatic HTTPS Rewrites: **ON**
- Minimum TLS Version: **1.2** (1.3 был бы строже, но iOS до 12 умеет только 1.2)

### Шаг 4. Security — отключить агрессивные фичи

**Это критично для VPN-проекта:** xray-клиенты (v2rayN, Hiddify, Streisand, sing-box) ходят за подпиской `cdn.example.com/api/v1/subscription/<token>` с нестандартным User-Agent → CF их режет → **подписки не обновляются**.

| Настройка | Значение | Где найти |
|---|---|---|
| **Browser Integrity Check** | **OFF** | `Security → Settings` |
| **Bot Fight Mode** | **OFF** | `Security → Bots` |
| **Security Level** | `Essentially Off` или `Low` | `Security → Settings` (для свежих зон может быть deprecated — пропустить) |

### Шаг 5. Speed / Caching (необязательно, но полезно)

- **Speed → Optimization → Brotli:** ON (часто уже включён по умолчанию)
- **Caching → Configuration → Browser Cache TTL:** `Respect Existing Headers` (Caddy сам ставит правильные `Cache-Control` для `/_next/static/*` — пусть CF их уважает)

### Шаг 6. Переключить NS у регистратора

CF выдаёт 2 nameservers вида `xxx.ns.cloudflare.com`. Их нужно прописать у регистратора.

**Namecheap (наш случай):**
1. https://ap.www.namecheap.com/domains/list/ → **Domain List**
2. **Manage** напротив домена
3. Вкладка **Domain** → секция **NAMESERVERS**
4. Выбрать **Custom DNS** (вместо `Namecheap BasicDNS`)
5. Вписать оба `*.ns.cloudflare.com` в инпуты
6. **Зелёная галочка ✓ справа** — обязательно ткнуть, иначе не сохранится (типичная ошибка)

**Сохрани текущие NS перед переключением** (скриншот) — на случай отката.

### Шаг 7. Дождаться пропагации

- Обычно 5-30 минут, иногда до 24ч
- Проверка:
  ```bash
  dig NS example.com +short @1.1.1.1
  ```
  должно показать `*.ns.cloudflare.com`
- В CF на странице зоны статус сменится `Pending Nameserver Update` → **Active** (зелёная плашка)
- Кнопка `Check nameservers now` в CF ускоряет проверку

### Шаг 8. Финальный e2e-тест

Запускать **с любой машины КРОМЕ origin-VPS** (на самом VPS curl попадёт прямо в локальный Caddy минуя CF, и тест будет неинформативным):

```bash
# 1. Frontend (Mini App)
curl -s -o /dev/null -w "cdn:  HTTP %{http_code}  via %{remote_ip}\n" https://cdn.example.com/

# 2. API health
curl -s -o /dev/null -w "api:  HTTP %{http_code}  via %{remote_ip}\n" https://api.example.com/health

# 3. Subscription endpoint с UA xray-клиента (проверка что BIC/Bot Fight не блочат)
curl -s -o /dev/null -w "sub:  HTTP %{http_code}  via %{remote_ip}\n" \
  -H "User-Agent: v2rayN/6.0" \
  https://cdn.example.com/api/v1/subscription/test

# 4. HTTP → HTTPS редирект
curl -s -o /dev/null -w "http: HTTP %{http_code}  Location: %{redirect_url}\n" \
  http://cdn.example.com/
```

Что должно получиться:

| Тест | Ожидаемо | Что значит если не так |
|---|---|---|
| `cdn:` HTTP 200, IP `104.21.x.x` или `172.67.x.x` или `188.114.x.x` | ✅ | Если IP origin (`178.104.217.201`) — Proxy не включён или NS ещё не пропагировались |
| `api:` HTTP 200 | ✅ | Если 525 — серт на origin не валиден, переключи Full (strict) → Full и разбирайся |
| `sub:` HTTP **404** (или 401) | ✅ — origin gateway честно отвечает | Если **403** — это уже CF блочит, что-то из BIC/Bot Fight ещё включено |
| `http:` HTTP 301/308 → `https://...` | ✅ Always Use HTTPS работает | |

---

## 🛠 Что добавлять в Caddyfile

### Минимум — `api` + `cdn`

Уже есть в <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/deploy/compose/caddy/Caddyfile" />:

```caddyfile
{$API_DOMAIN} { ... reverse_proxy {$GATEWAY_UPSTREAM} ... }
{$CDN_DOMAIN} { ... reverse_proxy {$NEXT_UPSTREAM} ... }
```

Домены берутся из `vpn_go/deploy/env/.env` (см. `API_DOMAIN`, `CDN_DOMAIN`). Чтобы поменять домен — правка `.env` + `docker compose up -d caddy`, серт выпустится автоматически.

### Опционально — apex/www → редирект на cdn

Если хочешь чтобы голый домен открывал Mini App, добавь в Caddyfile:

```caddyfile
example.com, www.example.com {
    redir https://cdn.example.com{uri} permanent
}
```

Соответственно DNS: apex и www → `A <ORIGIN_IP>`, оба 🟠 Proxied.

---

## ⚠️ Типичные грабли

### 1. CF поставил Proxy 🟠 на ВСЕ A-записи при импорте
По умолчанию CF проксирует всё — это **сломает VPN-ноды**. **Обязательно** пройдись и сними Proxy с любых записей которые ведут на xray/Reality.

**Симптом:** старые VLESS-конфиги перестают подключаться сразу после переключения NS. Юзеры пишут «не работает».
**Фикс:** снять 🟠 на всех нодовых записях.

### 2. 522 Connection timed out
CF не достучался до origin. Причины:
- UFW/iptables на origin блочит CF IP-диапазоны → разрешить https://www.cloudflare.com/ips/
- Origin лёг (проверь `docker ps` на VPS)
- Не тот A-запись (указывает на старый/удалённый сервер)

### 3. 525 SSL handshake failed
Только при Full (strict). Серт на origin невалидный или просрочен.
- Проверь `curl --resolve` (см. Шаг 3)
- Если Caddy на staging-CA — переключить на prod LE
- Откати на Full (без strict) пока чинишь

### 4. 403/Forbidden или CAPTCHA от CF
BIC, Bot Fight Mode или Security Level режут не-браузерные клиенты.
- Browser Integrity Check → **OFF**
- Bot Fight Mode → **OFF**
- Security Level (если есть) → **Essentially Off**

### 5. Хедер `cf-ray` не появляется в ответе
- Запрос идёт мимо CF: проверь `dig +short api.example.com @1.1.1.1` — должны быть CF anycast IP
- Если запрашиваешь **с самого origin-VPS** — curl попадёт в локальный Caddy через `/etc/hosts` или прямой IP. **Тестируй с другой машины.**

### 6. Email forwarding отвалился
Если в CF не импортированы или удалены `MX` + `SPF TXT` — Namecheap email forwarding не работает.
**Фикс:** вернуть MX-записи (`eforward1-5.registrar-servers.com`, приоритеты 10/10/10/15/20) и SPF TXT в DNS only.

---

## 🔐 Безопасность origin (опционально, но рекомендуется)

После того как CF проксирует домен — желательно закрыть прямой доступ к origin:

### Вариант A: разрешить только CF IP на 443

В UFW на origin VPS:
```bash
# Скачать актуальный список CF IP-диапазонов
for ip in $(curl -s https://www.cloudflare.com/ips-v4); do
    ufw allow proto tcp from $ip to any port 443
done
ufw deny 443/tcp  # всё остальное — запретить
```

Минус: если кто-то знает IP origin (`178.104.217.201`) — `curl --resolve` всё ещё работает. Не критично, но прямой доступ заблокирован.

### Вариант B: Authenticated Origin Pulls (mTLS)

CF выдаёт клиентский серт, Caddy проверяет его на каждом запросе. Только запросы с валидным CF-сертом проходят. Сложнее настраивать, но самый строгий вариант.

Пока на проекте не внедрялось — apex и api открыты на любой IP.

---

## 📚 Ссылки

- [CF docs — Add a domain](https://developers.cloudflare.com/dns/zone-setups/full-setup/setup/)
- [CF SSL/TLS modes](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/)
- [CF IP ranges](https://www.cloudflare.com/ips/)
- [Namecheap — Custom DNS](https://www.namecheap.com/support/knowledgebase/article.aspx/767/10/how-to-change-dns-for-a-domain/)
- [04-caddy-auto-tls.md](./tasks/04-caddy-auto-tls.md) — почему изначально шли без CF и как настроен Caddy auto-TLS
