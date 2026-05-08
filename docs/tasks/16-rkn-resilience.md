# 16. RKN-устойчивость подписки и нод (multi-domain + RU-mirror + ротация)

**Дата:** 2026-05-07
**Статус:** 🟢 Stage 2 (RU-mirror подписки) — в работе с 2026-05-08. Конфиги: `vpn_go/deploy/ru-mirror/`.
**Автор:** Devin + aziz
**Связано:**
- [04-caddy-auto-tls.md](./04-caddy-auto-tls.md) — текущий single-domain Caddy + LE
- [12-sni-rotation.md](./12-sni-rotation.md) — ротация SNI на уровне Reality (другая плоскость защиты)
- [../cloudflare.md](../cloudflare.md) — текущее состояние CF для `osmonai.com`

---

## 🎯 Цель

Снизить риск тотального недоступа сервиса при **блокировке `cdn.osmonai.com` / `api.osmonai.com` Роскомнадзором** или при **бане CF-аккаунта/IP-диапазонов**. Подписка и Mini App должны продолжать работать у юзера в РФ когда:
1. РКН добавил `cdn.osmonai.com` в реестр (DNS-блок у магистральных провайдеров)
2. РКН блочит CF anycast-диапазон в моменте (происходит периодически)
3. CF забанил аккаунт за нарушение ToS (VPN — серая зона)
4. Один из VPN-нод-IP попал в реестр

**Не входит в этот тикет:** обход блокировок самим VPN-трафиком (это решает Reality + SNI-rotation, см. [12-sni-rotation.md](./12-sni-rotation.md)). Тут речь только о **control-plane** — подписке, Mini App и API.

---

## 📚 Контекст

### Текущая инфра

- `api.osmonai.com` + `cdn.osmonai.com` → один VPS `178.104.217.201` (Финляндия, single point of failure для control-plane)
- Оба за CF Proxy (после задачи 04 + миграции на CF DNS 2026-05-07)
- VPN-ноды (`204.168.248.33` и т.д.) → DNS only / прямые IP в клиентских конфигах
- Регистратор `osmonai.com` — Namecheap

### Что делают конкуренты (анализ 2026-05-07)

| Сервис | Где подписка | Хостинг | CDN | Регистратор |
|---|---|---|---|---|
| hwpn.net | `hwpn.net/token/*` | Tencent SG | **Tencent EdgeOne** (анти-DDoS CDN) | Namecheap + CF DNS only |
| sigmalink.org | `www.sigmalink.org` | **Webhost LLC, RU** | нет (голый nginx) | Tucows + Njalla NS (anon) |
| extravpn.info | `sub.extravpn.info` | **Filanco LLC, RU** | нет | retry.name (anon) |
| lidervpn.com | `sub.lidervpn.com` | **Beget DC, RU** | нет (Caddy) | Beget LLC (RU) |

**Ключевой вывод:** 3 из 4 хостят подписку **внутри РФ**. Это не баг — это фича. Подписка не несёт VPN-трафика (только конфиг/JSON), РКН свою территорию не блочит → доступность 100%. Риск — RKN-жалоба провайдеру (Beget/Webhost регулярно отдают VPN-сервисы).

Возраст доменов конкурентов: 2-12 месяцев. Никто не «долгожитель» — выживание = ротация + анонимность регистратора.

---

## 🏗 Решение: 3 уровня защиты

### Уровень 1: RU-mirror подписки (главное)

Параллельно `cdn.osmonai.com` поднять **RU-зеркало** на отдельном домене + RU-VPS:

- Регистратор: **Reg.ru** или **Beget** (RU)
- Домен: `osmonai.ru` или скрытый `s-<random>.ru` (не светить публично)
- VPS: **Beget Cloud** / **Selectel** / **Timeweb** — тариф ~200-400₽/мес
- На VPS — `vpn-next` контейнер (та же Mini App) + reverse-proxy на тот же `gateway` через приватный туннель (WireGuard / Tailscale до origin VPS)
- **Не за CF** — голый Caddy + LE (через RU-провайдера LE доступен без проблем)

**Принцип:**
```
[ Юзер в РФ ]
     │
     ├─── обычный путь ──→ cdn.osmonai.com (CF Proxy → 178.104.217.201)
     │
     └─── fallback ──→ s-osmonai.ru (RU-VPS → tunnel → 178.104.217.201:8081)
```

Юзер в Mini App видит **выбор подписки**: CF-вариант (быстрее, скрыт за CDN) vs RU-вариант (медленнее, но всегда доступен). Можно отдавать обе ссылки клиенту в формате `subscription_url` + `subscription_url_ru_fallback`.

### Уровень 2: Multi-domain rotation для подписки

Иметь **N ≥ 3** доменов на подписку, ротация через UI/Mini App при необходимости:

| Домен | Регистратор | NS | Хостинг | Сценарий использования |
|---|---|---|---|---|
| `cdn.osmonai.com` (текущий) | Namecheap | CF | FI (CF Proxy) | основной для зарубежья и пока работает в РФ |
| `s.osmonai.ru` (новый) | Reg.ru/Beget | Beget/Reg.ru | Beget DC, RU | RU-fallback |
| `<random>.<tld>` (резерв) | Njalla (anon) | Njalla | сменный | если предыдущие два упали — выкатываем третий |

Каждые 3-6 месяцев — **новый резервный домен**. Старые не убираем (юзеры со старыми ссылками продолжают работать пока не заблочены).

В клиенте (Mini App) подписочный URL **обновляется при каждом запросе** `/api/v1/subscription/<token>`: gateway смотрит откуда пришёл запрос (`api.osmonai.com` vs `s.osmonai.ru`) и в `Profile-Web-Page-URL` отдаёт **тот же домен** что был в запросе. Тогда юзер не теряет «свой» путь.

### Уровень 3: Скрытие реального origin IP `178.104.217.201`

Сейчас если RKN блочит CF — origin IP всё равно достижим напрямую. Но **зная IP**, RKN может добавить и его в реестр напрямую (минуя домен).

Варианты:
- **A. UFW allow только CF IP** на :443 — закрывает прямой доступ. Минус: ломает RU-mirror (придётся либо открывать RU-VPS IP отдельно, либо туннелировать)
- **B. Authenticated Origin Pulls** (mTLS между CF и Caddy) — тот же эффект, строже. Сложнее настраивать
- **C. Cloudflare Tunnel** (`cloudflared`) — TCP outbound от origin к CF, на origin вообще не нужен открытый порт. Радикально, но завязывает на CF (если CF забанит — сервис лежит)

Рекомендация — **A** для main domain, RU-VPS IP добавить в whitelist отдельно. Внедрять **после** RU-mirror.

---

## 🛡 Защита нод (отдельный аспект)

VPN-ноды (`204.168.248.33` и будущие) — отдельная плоскость:

1. **IP не светить в DNS публично** — в идеале только в клиентских конфигах (так уже сделано: `vpn_data_json/wisekeys/*.json` содержат IP, не домены)
2. **Pool IP'ов на каждую region** — заранее 3-5 VPS у разных провайдеров (UpCloud, Hetzner, Aeza, Stark)
3. **Auto-rotation в gateway** — при подозрении на блокировку конкретного IP (метрика `connection_failures` подскочила) → автомат отключает IP из выдачи в `subscription_routing.go`, юзеры на следующем `/api/v1/subscription/*` получат новый список без блокированного IP
4. **Healthcheck pinger** — cron из gateway проверяет коннективность нод (не просто `ping`, а `tcp:8443` через Reality-handshake mock) → отключает упавшие

Это **отдельная подзадача** — будет описана как `tasks/17-node-rotation.md`. Здесь только control-plane.

---

## 📦 Этапы реализации

### Stage 1 — Анонимность регистратора (быстро)

- [ ] Зарегать **anonymous WHOIS** на `osmonai.com` в Namecheap (Privacy Protection — free)
- [ ] **Включить 2FA** на Namecheap-аккаунте (если не включено)
- [ ] **Включить 2FA** на Cloudflare-аккаунте

### Stage 2 — RU-mirror (минимальная: только подписочный эндпоинт)

Решение от 2026-05-08: домен — `s.osmonai.com` (subdomain в текущей CF-зоне,
DNS-only, БЕЗ proxy). Провайдер VPS — **Timeweb**. Mini App + API остаются
на FI-стенде, в РФ зеркалим **только** `/api/v1/subscription/*` (легче
поднять, меньше attack surface, всё что не подписка — 404 на RU).

- [x] `subscription_config.go:150` — `Profile-Web-Page-URL` теперь динамический (`resolveRequestBaseURL`, X-Forwarded-Host first). `subscription_url` в `/vpn/subscription-token` остался на `resolvePublicBaseURL` (env first) — переключается одной env-переменной.
- [x] `deploy/ru-mirror/` — Caddyfile + docker-compose.yml + .env.example + setup.sh + wireguard.md + README
- [x] `deploy/compose/docker-compose.ru-mirror.yml` — override который биндит gateway на `10.13.13.1:8081` (WG-IP)
- [x] VPS Timeweb куплен: `72.56.247.97` (Ubuntu 24.04 LTS, 2GB RAM)
- [x] CF: A `s.osmonai.com → 72.56.247.97`, DNS only ✅
- [x] WireGuard-туннель `10.13.13.0/24` развёрнут: FI=10.13.13.1, RU=10.13.13.2, UDP 51820, handshake ~50ms
- [x] FI gateway бинднут на `10.13.13.1:8081` через override
- [x] RU Caddy `caddy:2-alpine` поднят на `s.osmonai.com`, LE prod cert получен (tls-alpn-01)
- [x] Smoke: `s.osmonai.com/api/v1/subscription/<token>` → 200 + `Profile-Web-Page-URL: https://s.osmonai.com` ✅
- [x] Smoke: `cdn.osmonai.com/api/v1/subscription/<token>` → 200 + `Profile-Web-Page-URL: https://cdn.osmonai.com` ✅ (старый путь продолжает работать)
- [ ] FI env: `PUBLIC_BASE_URL=https://s.osmonai.com` → новые `subscription_url` идут на RU **(сделать когда CF реально упадёт у юзеров; сейчас RU-mirror работает как warm standby)**

**Состояние:** Stage 2 задеплоен, RU-mirror готов как hot-standby. Старая ссылка `cdn.osmonai.com/api/v1/subscription/...` работает. Новая `s.osmonai.com/api/v1/subscription/...` тоже работает. Обе self-consistent: каждая отдаёт свой хост в `Profile-Web-Page-URL`. Если CF упадёт в РФ — переключаем `PUBLIC_BASE_URL` env на FI и `docker compose up -d gateway`, новые юзеры получают RU-URL. Старые клиенты с CF-URL могут продолжать работать (CF может частично работать у части провайдеров) или вручную перевыпустить subscription-token.

Полный `Mini App + API + sub` mirror отложен (это уже Stage 2-extended /
`tasks/08-ha-backend-mirror.md`). Возвращаемся когда:

- появятся жалобы что `cdn.osmonai.com` лежит у российских провайдеров
- или Mini App перестанет открываться (что маловероятно — apex `osmonai.com` за CF)

### Stage 3 — Резервный домен

- [ ] Купить через Njalla (≈€15/год, BTC-оплата, anon WHOIS)
- [ ] Зарегать на anonymous email (ProtonMail/Tuta)
- [ ] **Не использовать сразу** — держать «в кармане» как резерв

### Stage 4 — Origin IP whitelist

- [ ] Скрипт `deploy/scripts/cf-ip-whitelist.sh` — обновляет UFW правила из <https://www.cloudflare.com/ips-v4>
- [ ] Cron на origin VPS — еженедельный refresh whitelist
- [ ] Добавить RU-VPS IP в whitelist отдельной строкой

### Stage 5 — Кнопка «не работает подписка?» в Mini App

- [ ] В UI Mini App — `Settings → Failover → Switch to RU mirror` (или auto при network error)
- [ ] Сохранять предпочтение в localStorage / Telegram WebApp storage
- [ ] Telemetry: лог `subscription_url_used = cf|ru|reserved` в `events` для аналитики

---

## 🚫 Что делать НЕ нужно

| Идея | Почему отказались |
|---|---|
| Cloudflare Tunnel вместо direct VPS | Жёсткая зависимость от CF — забанят аккаунт = всё лежит. RU-mirror тоже не получится сделать из туннеля |
| Перенести **всю** подписку в РФ (отказ от CF) | CF даёт быструю доставку статики Next.js по миру — зарубежные юзеры не должны страдать. Multi-domain лучше |
| Скрыть `osmonai.com` (apex) | Бесполезно — RKN ищет по subdomain'у `cdn.*`, apex и так пустой |
| ICANN proxy registrant (Whois Privacy через регистратора) | Стандартная фича, уже работает на Namecheap. Не путать с anonymous registrar (Njalla) |

---

## 📊 Risk-matrix после внедрения

| Сценарий | До задачи | После задачи |
|---|---|---|
| RKN блочит `cdn.osmonai.com` | 100% даун в РФ | RU-mirror работает, юзер автоматически переключается |
| RKN блочит CF anycast IP | большинство РФ-юзеров теряют доступ | RU-mirror работает |
| CF банит аккаунт | 100% даун (всем) | RU-mirror работает; на резервный домен переезжаем за час |
| Beget баннит RU-VPS по жалобе | RU-mirror лежит, но `cdn.osmonai.com` живой → большинство ок | поднимаем на Selectel |
| Утёк origin IP `178.104.217.201` в реестр RKN | прямой DNS-запрос к 178.104.217.201:443 заблочен у магистралов | CF Proxy продолжает работать (RKN не блочит CF целиком) + RU-mirror через WG-туннель |

---

## 🔗 Что трогать в коде

| Файл | Что менять |
|---|---|
| `services/gateway/internal/handler/subscription_config.go` | `Profile-Web-Page-URL` — динамический host |
| `services/gateway/internal/handler/telegram_bot.go` | Кнопка «Открыть Mini App» — массив URL'ов с fallback |
| `services/gateway/internal/handler/telegram_bot_promo.go` | `promoMiniAppBase` — превратить в slice или env-список |
| `vpn_next/app/connect/page.tsx` | Логика fetch с retry на разные subscription_url |
| `deploy/env/.env.template` | `CDN_DOMAIN_LIST=cdn.osmonai.com,s.osmonai.ru` |
| `deploy/compose/caddy/Caddyfile` | Multi-domain блок если main VPS обслуживает несколько |
| `deploy/scripts/cf-ip-whitelist.sh` | Новый |

---

## 📅 Когда делать

После закрытия:
- [02-mvp-c-implementation.md](./02-mvp-c-implementation.md) — основной MVP
- [12-sni-rotation.md](./12-sni-rotation.md) — SNI-rotation (защита самого VPN-трафика, более срочно)
- ~50+ платящих юзеров — пока меньше, RKN скорее всего не заметит и можно отложить

Stage 1 (anonymous WHOIS + 2FA) — **сделать сейчас**, это бесплатно и моментально снижает риск.
