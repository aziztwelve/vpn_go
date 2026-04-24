# 03. Клиентская конфигурация с «умным» routing'ом

**Дата:** 2026-04-23
**Статус:** 🟢 Код готов — осталось E2E-тест на телефоне (делает Aziz)
**Автор:** Devin + aziz
**Родительский:** [02-mvp-c-implementation.md](./02-mvp-c-implementation.md)

---

## 🎯 Цель

Сейчас VPN Service отдаёт клиенту только голую `vless://`-ссылку (`GetVLESSLink` в `services/vpn-service/internal/service/vpn.go:204`). Клиентское приложение (v2rayNG, Streisand, FoXray, Hiddify) применяет **дефолтный роутинг** — «всё через прокси». На практике этого мало:

- Российские банки, госуслуги, маркетплейсы ломаются при доступе с иностранного IP (антифрод, geo-гейтинг).
- Apple Push Service рвёт соединение при смене сети/гео → push-уведомления пропадают.
- BitTorrent через наш VPS — дорого по трафику и создаёт риск жалоб хостеру.
- Российские CDN (Yandex, VK) быстрее работают **напрямую**, а не через Финляндию.

**Результат:** пользователь думает, что «VPN сломал интернет», и уходит.

Цель задачи — научиться отдавать **полноценный Xray-конфиг** (а не только VLESS-ссылку) с готовым split-tunnel routing'ом «РУ → direct, остальное → proxy».

---

## 📚 Контекст (reference)

В чате был разобран клиентский конфиг коммерческого VPN (iOS/Streisand):

```json
{
  "inbounds":  [{ "protocol":"socks", "port":10808 }, { "protocol":"http", "port":10809 }],
  "outbounds": [
    { "tag":"proxy",  "protocol":"vless", ... },
    { "tag":"direct", "protocol":"freedom" },
    { "tag":"block",  "protocol":"blackhole" }
  ],
  "routing": {
    "rules": [
      { "protocol":["bittorrent"],          "outboundTag":"direct" },
      { "ip":[vk/yandex CIDR …],            "outboundTag":"direct" },
      { "domain":[vk.com, ozon, wb, alfa, gosuslugi, apple, icloud, ~200 домен], "outboundTag":"direct" },
      { "ip":[17.0.0.0/8, банк-CIDR, ...],  "outboundTag":"direct" }
      // fallback → proxy
    ]
  }
}
```

Ключевые приёмы, которые надо повторить:

| Приём | Зачем |
|---|---|
| `sniffing.destOverride: [http, tls, quic]` на inbound | домен-правила работают даже для TCP-к-IP |
| `queryStrategy: UseIPv4` + per-domain DNS (Yandex DNS для `geosite:category-ru`) | не ловить IPv6-утечки + быстрый РУ-резолв |
| uTLS `fingerprint` (chrome/qq/ios) | анти-DPI по ClientHello |
| Порядок правил: protocol → IP → domain → fallback | bittorrent ловится до DNS; IP-правила быстрее domain |
| `geosite`/`geoip` дата-файлы | не ручной список, а подгружаемая база через ссылки/файлы |

Разница от reference: у нас **Reality**, а не обычный TLS + SNI trick → часть параметров (pbk/sid/shortId) заменит (fingerprint/serverName/cert-chain). См. `services/vpn-service/internal/service/vpn.go:204+` — там уже правильно генерим Reality-параметры для ссылки.

---

## 🗂 Что именно сделать (варианты, надо выбрать)

### Вариант A — Отдавать готовый `config.json` с сервера

Новый gRPC/HTTP endpoint: `GetClientConfig(user_id, server_id, profile)` → возвращает **полный JSON** Xray-конфига.

- **Плюс:** пользователю достаточно импортировать файл, всё работает «из коробки» в v2rayN/Nekoray/Xray-core CLI. Работает даже без фирменного клиента.
- **Минус:** многие мобильные клиенты (v2rayNG, Streisand) импортируют **ссылки**, а не JSON. Нужен дополнительный UX — «скачать .json» в Mini App.

### Вариант B — Subscription link (sub://)

Стандартный Xray/v2ray формат подписки: HTTP-ручка отдаёт **список base64-закодированных** `vless://`-ссылок + (опционально) YAML-профиль (Clash-style). Клиенты (Hiddify, Streisand, v2rayNG) умеют подписываться и **получают routing правила из профиля**.

- **Плюс:** нативно поддерживается всеми клиентами; юзер добавляет **одну ссылку** и получает все свои сервера + правила; мы можем обновлять правила на лету, клиент подтянет.
- **Минус:** каждый клиент интерпретирует routing чуть по-разному (v2rayNG слабее, Hiddify лучше всех). Полноценный split-tunnel работает не везде.

### Вариант C — Два уровня: ссылка (дефолт) + config.json (продвинутый)

- Дефолтно — как сейчас, `GetVLESSLink` → пользователь копипастит в клиент.
- Дополнительно кнопка «Продвинутые настройки» → `GetClientConfig` отдаёт полный JSON с routing'ом.

- **Плюс:** обратная совместимость, не ломаем текущий флоу.
- **Минус:** два пути поддерживать; среднему юзеру не объяснишь «скачай json, импортируй в Hiddify».

**Рекомендация:** начать с **B (subscription link)** — он ближе всего к тому, что делают коммерческие VPN. **A/C** как fallback для power-users.

---

## 🧱 Архитектурный план (для Варианта B)

### Новый HTTP-endpoint в Gateway

```
GET /api/v1/subscription/{token}
Authorization: не требуется (token сам по себе секрет, привязан к user_id)

Response 200: text/plain
<base64(vless://… + vless://… + …)>

Или Content-Type: application/yaml — Clash-style config
```

`token` — одноразовый UUID, хранится в `vpn_users.subscription_token` (новая колонка). Можно ротировать. URL можно давать в Mini App как QR.

### Источник правды для routing-правил

Список РУ-доменов/IP — это **внешние данные**, надо автоматически обновлять. Варианты:

| Источник | Плюсы | Минусы |
|---|---|---|
| [`v2fly/domain-list-community`](https://github.com/v2fly/domain-list-community) → `geosite.dat` | Стандарт, сотни категорий | Требует компиляции в `.dat`, тянуть ~2 МБ на клиент |
| [`runetfreedom/russia-domains-list`](https://github.com/runetfreedom/russia-domains-list) | Готовый домены-РУ + geoip | Меньше категорий |
| `antifilter.download` | Актуальные списки ФГИС + РКН | Спорная легитимность источника |
| Свой курируемый список в `deploy/routing/ru-direct.txt` | Полный контроль | Надо ручками обновлять |

**Решение:** на MVP — **свой файл `ru-direct.txt` + `ru-ip.txt`** в репо, обновляем раз в месяц из `runetfreedom`. Позже — фоновый cron в vpn-service, который скачивает geosite.dat.

### Компоненты

```
services/vpn-service/internal/
├── service/
│   └── client_config.go        ← NEW: BuildClientConfig(user, server, profile)
├── model/
│   └── routing_rules.go        ← NEW: RoutingRule, LoadFromEmbed
└── embed/
    ├── ru-domains.txt          ← NEW: список РУ-доменов (один per line)
    ├── ru-ips.txt              ← NEW: список РУ-CIDR
    └── routing.go              ← NEW: //go:embed — компилится в бинарь

services/gateway/internal/handler/
└── subscription.go             ← NEW: GET /api/v1/subscription/{token}

services/vpn-service/migrations/
└── NNN_add_subscription_token.up.sql    ← ALTER TABLE vpn_users ADD COLUMN subscription_token UUID UNIQUE
```

### Генерация config.json (псевдокод)

```go
func (s *Service) BuildClientConfig(user *VPNUser, servers []*Server, profile Profile) ([]byte, error) {
    cfg := ClientConfig{
        DNS: dnsBlock(profile),                  // UseIPv4 + Google/Yandex per-domain
        Inbounds: defaultInbounds(),             // socks 10808, http 10809
        Outbounds: append(
            []Outbound{{Tag: "proxy", ...vlessFromServer(servers[0], user)}},
            Outbound{Tag: "direct", Protocol: "freedom"},
            Outbound{Tag: "block",  Protocol: "blackhole"},
        ),
        Routing: Routing{
            Rules: buildRules(profile),          // см. ниже
        },
    }
    return json.MarshalIndent(cfg, "", "  ")
}

func buildRules(profile Profile) []Rule {
    rules := []Rule{
        {Protocol: []string{"bittorrent"}, OutboundTag: "direct"},
    }
    if profile.SplitTunnelRU {
        rules = append(rules,
            Rule{IP: embedRUIPs,      OutboundTag: "direct"},
            Rule{Domain: embedRUDomains, OutboundTag: "direct"},
        )
    }
    if profile.KeepApplePush {
        rules = append(rules, Rule{Domain: []string{"apple.com","icloud.com","push-apple.com.akadns.net"}, OutboundTag: "direct"})
    }
    return rules
}
```

`Profile` — настройка из подписки (`subscriptions.preferred_profile`?) или параметр ручки.

---

## ❓ Открытые вопросы (к aziz)

1. **Subscription link vs config.json** — какой вариант делаем первым (B / A / C)?
2. **Профили из коробки:** стартуем с одним «full-split-tunnel RU»? Или сразу дадим выбрать (full-tunnel / split-RU / split-RU+Apple / bitttorrent-on)?
3. **Источник списка РУ-доменов:** свой файл в репо или runetfreedom/geosite?
4. **Инвалидация токена:** автоматически при `DisableVPNUser` (подписка истекла) — да, очевидно. А при смене подписки (продлении)? Оставляем тот же токен, чтобы у юзера ссылка не менялась?
5. **Протокол отдачи:** plain base64 (стандарт v2ray) или Clash-YAML? v2ray-стандарт проще, Hiddify поддерживает оба.
6. **Мультисервер:** когда у юзера 3 сервера (RU, FI, NL) — в подписку идут **все три** outbound'а с `balancer`/`loadbalancer`, или только выбранный по `server_id`?
7. **Reality + sniffing:** Reality может конфликтовать со `sniffing` на client-side (рассинхрон SNI). Надо проверить на iOS Streisand — не ломается ли handshake.
8. **Rate-limit** ручки `/subscription/{token}`: публичная, можно ддосить. Добавляем `chi httprate` (10 req/min per IP)? → пересечение с Cross-cutting TODO «Rate limiting в Gateway».

---

## ✅ Definition of Done (черновой)

- [ ] Миграция: `vpn_users.subscription_token UUID UNIQUE` + auto-gen при `CreateVPNUser`
- [ ] Go-модуль `internal/routing/` с `//go:embed ru-domains.txt`, `ru-ips.txt`
- [ ] `BuildClientConfig(user, server, profile) → []byte` с юнит-тестом (snapshot-тест JSON)
- [ ] HTTP ручка в Gateway: `GET /api/v1/subscription/{token}` → base64 VLESS links **или** application/json с полным конфигом (надо выбрать)
- [ ] Инвалидация токена в `DisableVPNUser`: token → NULL, ручка возвращает 404
- [ ] E2E-тест: создал юзера → импорт ссылки в Hiddify на iPhone → `gosuslugi.ru` резолвится direct'ом (проверка через `curl --socks5`), `ipinfo.io` через proxy
- [ ] Rate-limit на ручке (пересекается с Cross-cutting TODO)
- [ ] Обновление `docs/services/xray-integration.md` — раздел «Клиентская сторона»

---

## 🔗 Ссылки

- Reference-конфиг коммерческого VPN — в истории чата 2026-04-23 (iOS Streisand, split-tunnel RU)
- [v2fly/domain-list-community](https://github.com/v2fly/domain-list-community)
- [XTLS Reality — клиентский fingerprint best practices](https://github.com/XTLS/REALITY)
- Текущая генерация VLESS-ссылки: `services/vpn-service/internal/service/vpn.go:204`
- Соседний TODO: «Re-seed Xray» в [02-mvp-c-implementation.md](./02-mvp-c-implementation.md#cross-cutting-todos)
