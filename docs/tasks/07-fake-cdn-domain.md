# 07. Фейк-CDN домен sbrf-cdn571.ru + гибридная инфра (RU backend + foreign exit-ноды)

**Дата:** 2026-04-24
**Статус:** ⏸ Отложено — у нас уже есть Reality+SNI=github.com (см. `docs/SUBSCRIPTION.md`), который покрывает ту же задачу элегантнее. Возвращаемся если нужен fallback на чистый TLS+VLESS.
**Автор:** Aziz + Devin
**Родительский:** [02-mvp-c-implementation.md](./02-mvp-c-implementation.md) — заменяет подпункт «публичный домен» из Этапа 9
**Связанные:**
- [04-caddy-auto-tls.md](./04-caddy-auto-tls.md) — Caddy auto-TLS для api/sub поддоменов
- [08-ha-backend-mirror.md](./08-ha-backend-mirror.md) — High Availability backend (failover на зарубежное зеркало) — **делаем после стабилизации MVP**

---

## 🎯 Цель

Поднять production-инфру VPN-сервиса по паттерну [Wisekeys VPN](../../vpn_data_json/wisekeys/) — домен маскируется под российский CDN («Сбер CDN»), разные поддомены указывают на разные страны, подписки отдаются через `happ://add/https://sub.sbrf-cdn571.ru/<token>`.

**Выгоды:**
1. Домен выглядит как легитимный рос. CDN → меньше подозрений у DPI, меньше риск блокировки
2. Один клик подписки через HAPP/v2rayNG/Streisand
3. SNI-камуфляж для VLESS/TLS (`serverName: sub.sbrf-cdn571.ru`)
4. IP VPS не светится в публичных конфигах — клиент видит только домен

## 📚 Контекст и ключевые решения

- **Купленный домен:** `sbrf-cdn571.ru` (на reg.ru, Aziz)
- **Референс:** `vpn_data_json/wisekeys/*.json` — wisekeys юзает `sbrf-cdn342.ru` (спрятан за MWS CDN, Angie-сервер, LE-cert)
- **Выбранная архитектура:** **гибрид** — backend на reg.ru VPS в РФ, exit-ноды Xray на загран-VPS
- **Выбранный протокол:** обычный TLS + VLESS (как у wisekeys, не Reality) — проще, совместим с любым клиентом
- **Формат подписки:** JSON для HAPP (пример: `vpn_data_json/wisekeys/Германия.json`)

## ⚠️ Риски выбранной архитектуры

Backend в РФ удобен (быстро для юзеров, всё в одном кабинете reg.ru), но имеет уязвимости:

| Угроза | Что произойдёт | Митигация |
|---|---|---|
| РКН блочит IP backend'а | Mini App не открывается, новые юзеры не могут зарегаться, обновления конфигов рвутся | См. таск [08](./08-ha-backend-mirror.md) — failover на зарубежное зеркало |
| reg.ru сносит наш VPS по требованию | Полный downtime, потеря БД если без бэкапа | Холодный бэкап (этап 8 ниже) + переезд на загран-VPS |
| Telegram блокируется в РФ | Бот недоступен (требует TG-клиент), но backend работает | Альтернативный канал регистрации (web-форма, Matrix bot) — будущая задача |
| Аппаратный сбой VPS | Downtime пока reg.ru поднимет | reg.ru SLA + бэкап на S3 |

**Важно про самодостаточность конфигов:** даже при падении backend'а — уже подключённые юзеры работают через exit-ноды напрямую (HAPP-конфиг self-contained). Backend нужен только для: новых регистраций, обновлений конфига, оплат. Это даёт буфер 24-72ч.

**Стратегия mitigation по этапам:**
1. MVP (этот таск) — single backend в РФ + **обязательный** холодный бэкап (см. этап 8)
2. После 100+ юзеров или первого инцидента — реализовать [таск 08](./08-ha-backend-mirror.md) (HA с активным mirror в DE)
3. Далее — рассмотреть переезд primary за рубеж совсем

## 🏗 Архитектура

```
                          sbrf-cdn571.ru  (NS: reg.ru, A-records)
                                       │
       ┌───────────────────────────────┼────────────────────────────┐
       │                               │                            │
  sub.sbrf-cdn571.ru          api.sbrf-cdn571.ru           de.sbrf-cdn571.ru
  (подписки + decoy)          (Gateway API + bot webhook)  (exit 🇩🇪)
       │                               │                            │
       └──────────────┬────────────────┘                            │
                      ▼                                             ▼
       ┌─────────────────────────────────┐        ┌──────────────────────────────┐
       │  🇷🇺 reg.ru VPS (backend, root)  │        │  🇩🇪 VPS (Aeza/Hetzner)       │
       │  ─ Caddy :80/:443 (auto-TLS LE) │        │  ─ Xray :443                  │
       │    • sub.sbrf-cdn571.ru         │        │  ─ VLESS+TLS                  │
       │      → decoy + /<token>         │        │  ─ certbot LE auto            │
       │    • api.sbrf-cdn571.ru         │        │  ─ SNI: sub.sbrf-cdn571.ru    │
       │      → gateway:8081             │        │    (камуфляж)                 │
       │  ─ Gateway (:8081)              │        │                               │
       │  ─ Auth/Subscription/Payment    │        └──────────────────────────────┘
       │    (gRPC)                       │
       │  ─ Postgres                     │         ┌──────────────────────────────┐
       │  ─ Redis (если нужен)           │         │  🇹🇷 VPS (tr.sbrf-cdn571.ru)  │
       │  ─ Мини-админка/метрики        │         │  ─ аналогично DE             │
       └─────────────────────────────────┘         └──────────────────────────────┘

                                                    + NL, PL, RS ... — по мере роста
```

**Ключевое**: подписки раздаёт сам gateway через Caddy (не нужен SFTP/WebDAV), `sub.sbrf-cdn571.ru/<token>` — это обычный HTTP endpoint в gateway, который отдаёт JSON-конфиг.

## 🧩 Этапы

### Этап 1. DNS в reg.ru (1ч)

- [ ] В панели reg.ru у домена `sbrf-cdn571.ru` проверить/настроить делегирование на NS reg.ru
- [ ] Создать A-записи (после покупки backend VPS, см. Этап 2):
  - `@` (apex) → IP backend VPS
  - `www` → IP backend VPS
  - `sub.sbrf-cdn571.ru` → IP backend VPS
  - `api.sbrf-cdn571.ru` → IP backend VPS
  - Поддомены стран добавляем по мере покупки VPS: `de.`, `tr.`, `nl.`, `pl.`
- [ ] TTL: 300
- [ ] Проверка: `dig +short sub.sbrf-cdn571.ru` → IP

### Этап 2. Backend VPS на reg.ru 🇷🇺 (3-4ч)

- [ ] Заказать reg.ru VPS (Москва/СПб):
  - Минимум: 2 CPU, 2 GB RAM, 30 GB SSD, Ubuntu 22.04/24.04
  - Ориентир: ~700-1500₽/мес
- [ ] Базовая настройка: non-root юзер, SSH по ключу, `ufw`, `fail2ban`, `unattended-upgrades`
- [ ] Поставить Docker + docker-compose
- [ ] Склонировать репо `vpn_go`, собрать образы (см. `deploy/compose/docker-compose.yml`)
- [ ] Настроить `.env` (Telegram bot token, JWT_SECRET, DB creds)
- [ ] Поднять Caddy из задачи [04-caddy-auto-tls.md](./04-caddy-auto-tls.md):
  - `api.sbrf-cdn571.ru` → `gateway:8081`
  - `sub.sbrf-cdn571.ru` → `gateway:8081` (тот же gateway, другие роуты)
- [ ] Запустить стек: `docker compose up -d`
- [ ] Проверить TLS: `curl https://api.sbrf-cdn571.ru/health`, `curl https://sub.sbrf-cdn571.ru/`

### Этап 3. Gateway: endpoints подписок + decoy (3-4ч)

- [ ] Decoy endpoint `GET /` на `sub.sbrf-cdn571.ru`:
  - Отдаёт простую страничку `<h1>CDN Service - Maintenance</h1>` (HTTP 200, не 502)
  - Чтобы случайный посетитель не видел подозрительного
- [ ] Endpoint `GET /<token>` на `sub.sbrf-cdn571.ru`:
  - Проверяет токен в БД (таблица `subscription_tokens`)
  - Если валидный + не expired → отдаёт JSON-конфиг HAPP (контент-тайп `application/json`)
  - Если нет → 404
- [ ] Endpoint `GET /api/v1/subscription/happ-link` (на `api.sbrf-cdn571.ru`):
  - Проверяет JWT, subscription активна
  - Генерирует token (rand 16 байт hex), кладёт в `subscription_tokens(token, user_id, country, expires_at)`
  - Возвращает `{"link": "happ://add/https://sub.sbrf-cdn571.ru/<token>"}`
- [ ] Миграция БД: таблица `subscription_tokens`
- [ ] Cleanup job (cron в gateway): удалять expired tokens раз в час

### Этап 4. JSON-шаблон конфига HAPP (2-3ч)

- [ ] Взять `vpn_data_json/wisekeys/Германия.json` как эталон
- [ ] Подставлять динамические поля:
  - `outbounds[0].settings.vnext[0].address` → `de.sbrf-cdn571.ru` (или tr./nl./...)
  - `outbounds[0].settings.vnext[0].users[0].id` → UUID юзера из `vpn_users`
  - `outbounds[0].streamSettings.tlsSettings.serverName` → `sub.sbrf-cdn571.ru` (общий SNI!)
  - `remarks` → `🇩🇪 Германия` / `🇹🇷 Турция`
- [ ] Routing rules (RU direct, split-tunnel) — взять готовые из wisekeys референса
- [ ] Уже частично есть код в `gateway/internal/handler/subscription_config.go:244,285` — пофиксить/расширить

### Этап 5. Первая exit-нода 🇩🇪 (3-4ч)

- [ ] Выбрать провайдера: **Aeza DE** (€3-5, принимает СБП/крипту) или **Hetzner DE** (€4.5, только IBAN/карта)
- [ ] Заказать VPS: 1 CPU, 1 GB RAM, 20 GB, Ubuntu 22.04
- [ ] Base hardening: non-root, SSH ключ, ufw (only 22, 443)
- [ ] A-запись: `de.sbrf-cdn571.ru → <IP>`, ждём propagation (5-15 мин)
- [ ] Поставить certbot + Xray:
  ```bash
  # Xray бинарь
  bash -c "$(curl -L https://github.com/XTLS/Xray-install/raw/main/install-release.sh)" @ install
  # Cert
  certbot certonly --standalone -d de.sbrf-cdn571.ru
  ```
- [ ] Конфиг `/usr/local/etc/xray/config.json`:
  - VLESS inbound на :443
  - TLS с LE-сертом `/etc/letsencrypt/live/de.sbrf-cdn571.ru/`
  - `clients`: пустой массив, пополняется через Xray API
- [ ] Включить Xray API (порт 10085, слушает только 127.0.0.1)
- [ ] systemctl enable xray
- [ ] Проверить: gateway подключается к Xray API, добавляет тестового юзера, клиент HAPP коннектится

### Этап 6. Интеграция в Mini App / бот (2ч)

- [ ] В vpn_next кнопка "Получить конфиг" → дёргает `/api/v1/subscription/happ-link`
- [ ] QR-код (уже есть qrcode-generator) + кнопка "Открыть в HAPP" (deep-link)
- [ ] В Telegram bot команда `/config` → тот же endpoint

### Этап 7. E2E тест (1-2ч)

- [ ] С реального телефона с HAPP:
  - Mini App → "Получить конфиг" → QR
  - Сканирую в HAPP → конфиг подхвачен
  - Коннект → `ipinfo.io` → IP Германии
  - RU-ресурсы (vk.com, yandex.ru) → напрямую (split-tunnel)
- [ ] Проверить что сразу несколько юзеров одновременно работают

### Этап 8. Холодный бэкап БД (2-3ч) — **ОБЯЗАТЕЛЬНО до запуска**

Минимальная защита от потери данных. Делаем сразу после Этапа 2, до приёма реальных юзеров.

- [ ] Завести аккаунт **Selectel S3** (или Yandex Object Storage) — ~50₽/мес за 5GB
- [ ] Cron на backend VPS:
  ```bash
  # /etc/cron.daily/backup-vpn-db
  #!/bin/bash
  set -e
  TS=$(date +%Y%m%d-%H%M%S)
  pg_dump -U vpn -d vpn -F c | gzip > /tmp/vpn-$TS.dump.gz
  aws s3 cp /tmp/vpn-$TS.dump.gz s3://vpn-backups/postgres/vpn-$TS.dump.gz \
    --endpoint-url https://s3.selectel.ru
  rm /tmp/vpn-$TS.dump.gz
  # Retention: оставить последние 30
  aws s3 ls s3://vpn-backups/postgres/ --endpoint-url https://s3.selectel.ru \
    | sort -r | tail -n +31 | awk '{print $4}' \
    | xargs -I {} aws s3 rm s3://vpn-backups/postgres/{} --endpoint-url https://s3.selectel.ru
  ```
- [ ] Тест восстановления: на отдельной машине развернуть последний дамп — убедиться что работает
- [ ] Документировать runbook восстановления в `docs/runbooks/restore-from-backup.md`
- [ ] Алерт в админский чат если последний бэкап старше 36ч

## ❓ Открытые вопросы

1. **Провайдер exit-VPS** — Aeza / Hetzner / Selectel / VPSVille? Зависит от метода оплаты (карта РФ / СБП / крипта / IBAN)
2. **Ротация token'ов подписки** — TTL 24ч (как wisekeys) или 7 дней? Пока ставим 7 дней
3. **Sub.domain per country** или `sub.sbrf-cdn571.ru` один на всех? Wisekeys: один. Оставляем один → все SNI одинаковые, меньше подозрительно
4. **Fronting через CDN** — как wisekeys за MWS CDN. В MVP не делаем, если забанят IP — меняем VPS
5. **Nameservers** — оставляем reg.ru NS или переезжаем на Cloudflare DNS (не прокси)? Reg.ru NS проще, CF даёт гибкость + метрики. Решаем по ходу.

## 🗓 Оценка

| Этап | Время | Зависимости |
|------|-------|-------------|
| 1. DNS | 1ч | — |
| 2. Backend VPS reg.ru | 3-4ч | этап 1, VPS заказан |
| 3. Gateway endpoints | 3-4ч | этап 2 |
| 4. JSON-шаблоны HAPP | 2-3ч | — (можно параллельно с 3) |
| 5. Exit VPS 🇩🇪 | 3-4ч | этап 1, VPS заказан |
| 6. Mini App/бот | 2ч | этап 3 |
| 7. E2E тест | 1-2ч | всё |
| 8. Холодный бэкап БД | 2-3ч | этап 2, S3 аккаунт |
| **Итого** | **17-23ч** | + ~2050₽/мес инфра на старте |

## 💰 Бюджет инфры

| Ресурс | Где | Цена (₽/мес) |
|---|---|---|
| Домен `sbrf-cdn571.ru` | reg.ru | ~200₽/год |
| Backend VPS 🇷🇺 | reg.ru | 700-1500 |
| Exit VPS 🇩🇪 (1-й) | Aeza/Hetzner | 300-500 |
| S3 для бэкапов БД | Selectel | ~50 |
| **Итого на старте** | | **~1050-2050/мес** |

Каждая новая страна → +300-500₽. При 500 юзерах ~10₽/юзер/мес на инфру.

**При апгрейде до HA** (таск 08) — добавляется +500₽/мес (DE mirror VPS), итого ~1500-2550/мес.

## 🔗 Ссылки

- Референс: [`vpn_data_json/wisekeys/`](../../vpn_data_json/wisekeys/)
- Родительский план: [02-mvp-c-implementation.md](./02-mvp-c-implementation.md)
- Auto-TLS: [04-caddy-auto-tls.md](./04-caddy-auto-tls.md)
- HAPP app: https://happ.su/
- Xray docs: https://xtls.github.io/
