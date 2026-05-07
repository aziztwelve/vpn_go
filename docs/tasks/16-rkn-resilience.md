# 16. RKN-устойчивость подписки и нод (multi-domain + RU-mirror + ротация)

**Дата:** 2026-05-07
**Статус:** 🟡 Черновик — обсуждение приоритетов
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

### Stage 2 — RU-mirror

- [ ] Купить домен `osmonai.ru` (Reg.ru, ~200₽/год)
- [ ] Арендовать VPS у Beget Cloud / Selectel (≤400₽/мес, 1vCPU/512MB достаточно)
- [ ] Развернуть `vpn-next` (Mini App) на RU-VPS — `docker compose up -d` идентичный prod
- [ ] WireGuard-туннель RU-VPS → FI-origin для приватного доступа к `gateway:8081`
- [ ] Caddy на RU-VPS: `s.osmonai.ru { reverse_proxy 10.13.13.1:8081 }` (через WG-туннель)
- [ ] DNS `s.osmonai.ru` → IP RU-VPS (без CF, голый A)
- [ ] Caddyfile → `gateway/internal/handler/subscription_config.go` доработка: возвращать `Profile-Web-Page-URL` динамически (тот же хост что в `Host:` запроса, не хардкод `https://cdn.osmonai.com`)
- [ ] Mini App env: `NEXT_PUBLIC_SUBSCRIPTION_URLS` — массив доменов, при ошибке fetch на первом — пробует второй

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
