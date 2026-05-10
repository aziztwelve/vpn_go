# end_sni — Финальный план SNI/RU-инфра (endgame)

**Дата:** 2026-05-10
**Статус:** 🟢 Утверждён — реализация по шагам
**Автор:** Devin + aziz
**Домен под VPN-ноды:** `qubexlabs.com` (Namecheap, куплен 2026-05-10)
**Источники:**
- [12-sni-rotation.md](./12-sni-rotation.md) — массив SNI per inbound
- [13-realitlscanner-donors.md](./13-realitlscanner-donors.md) — RealiTLScanner подбор доноров
- [17-ru-vps-lte.md](./17-ru-vps-lte.md) — RU-VPS для LTE (анализ конкурента geodataload.com)
- [16-rkn-resilience.md](./16-rkn-resilience.md) — RU-mirror подписки (✅ Stage 2 готов)

---

## 🎯 Что делаем

Поднять «промышленный» уровень обхода блокировок копируя архитектуру конкурента (`geodataload.com`), но **без его косяков** (общие ключи, один SNI на все ноды, плохой donor):

1. **Новый отдельный домен** под VPN-ноды (не subdomain `osmonai.com`).
2. **MWS Cloud RU-VPS** как `[LTE 2] Россия (мобильный)` inbound — серверная база уже есть у aziz.
3. **DNS round-robin** через Cloudflare DNS-only (TTL ~215c) → ротация IP без update подписок.
4. **4 donor SNI per inbound**, найденные RealiTLScanner-ом В ПОДСЕТИ ВПС (а не `apple.com`).
5. **Per-server Reality keys** (НЕ копируем reuse-схему конкурента).
6. **Hot-spare на Selectel** на случай бана MWS.

После реализации мы воспроизводим то, что у конкурента «просто работает», и закрываем 5 классов угроз сразу (мобильный DPI / SNI-аномалии / RKN-блок IP / slив ключа / связка с control-plane).

---

## 🏗 Архитектура

```
[ юзер LTE Хакасия ]
        │
        │ (1) DNS query: w1.qubexlabs.com   TTL 215c
        ▼
   ┌─────────────────────────────────────┐
   │ A-records (round-robin, CF DNS-only):│
   │  • <MWS-IP-1>      ← MWS RU         │  ← LTE-обход
   │  • <MWS-IP-2>      ← MWS RU         │     (если у MWS их 2+)
   │  • <Selectel-IP>   ← hot-spare RU   │     fallback
   └─────────────────────────────────────┘
        │
        │ (2) VLESS Reality :1443
        │ SNI = random из 4 donor'ов в подсети VPS
        │ (RealiTLScanner-pool, разный per VPS)
        ▼
   [ Xray на MWS ]  ─► freedom outbound  ─► интернет
```

---

## 📋 Этапы (последовательно)

### Stage 1 — Подготовка кода (ничего не ломаем) — ~1 рабочий день

Цель: код готов к multi-SNI и domain-based host. Пока деплой не трогаем.

- [ ] **Миграция 006** — `vpn_servers.server_names: TEXT → JSONB` (см. `12-sni-rotation.md` §1)
- [ ] `model.VPNServer.ServerNames: string → []string` + везде в `repository/vpn.go` `pq.Array(...)`
- [ ] `service/vpn.go:270` — `params.Add("sni", server.ServerNames[rand.Intn(len(...))])`
- [ ] Sync `deploy/schema.sql` с реальным состоянием БД (там сейчас `JSONB`-врёт)
- [ ] `deploy-xray-new.sh` — `serverNames` берёт массив через jq
- [ ] Тесты: добавить кейсы для рандом-выбора SNI
- [ ] Локально проверить: `vpn_servers` с массивом из 4 SNI → подписка отдаёт случайный

**Acceptance:** на dev-стенде `curl /api/v1/subscription/<token>` отдаёт VLESS-ссылку с РАЗНЫМИ `?sni=` при 5 запросах подряд.

### Stage 2 — Новый домен под VPN-ноды — ✅ ГОТОВО (2026-05-10)

Цель: VPN-ноды развязаны с `osmonai.com` (control-plane).

**Решение:** домен — **`qubexlabs.com`** (Namecheap, куплен 2026-05-10).
Критерии выбора были: `.com` TLD (минимальная категоризация в DPI/operator-feed против `.online/.xyz/.top`), нейтральное brandable имя без `vpn/proxy/tunnel/cdn/dns/secure/private`, чистая история (CT-empty, Wayback-empty), стабильная цена $11/год без «cheap-1st-year» накруток на ренью.

`maydavpn.online` остаётся под публичный лендинг/будущий маркетинг, под VPN-ноды НЕ используем (слово «vpn» + `.online` TLD уязвимы к keyword-фильтрации DNS на агрессивных LTE и категорийным блок-листам).

- [x] Купить домен → **`qubexlabs.com`** на Namecheap, Privacy Protection ON
- [x] **2FA на аккаунте Namecheap** + on Cloudflare (TOTP)
- [x] DNS на Cloudflare (бесплатный план), NS заменены у регистратора → `marjory.ns.cloudflare.com`, `bryce.ns.cloudflare.com`
- [x] Anonymous WHOIS (Namecheap WhoisGuard / Domain Privacy)
- [x] **Stub-лендинг** на `qubexlabs.com` через Cloudflare Pages — narrative «Qubex Labs — distributed observability research», без аналитики/кук/форм/JS, system-fonts only, ~8KB total. Исходники: `vpn/qubexlabs-landing/` (НЕ в `vpn_go` — отдельно от чувствительной репы).
  - Apex + www: HTTP 200 для browser/curl/Googlebot UA — категоризаторы (Cisco Talos / FortiGuard / Bluecoat) при scrape получают `Business / IT`-страницу, а не challenge/uncategorized
  - TLS edge-cert: Google Trust Services, auto-renew через CF
- [ ] **DNS-only режим** (без CF Proxy) — применится в Stage 4 для `w1.*` (Reality несовместим с CF Proxy). Apex/www наоборот ОСТАВЛЕНЫ под Proxy (оранжевое) — это правильно для лендинга.
- [ ] **TTL 215 секунд** на A-records — применится в Stage 4 при создании `w1`-записи

**ВАЖНО (gotcha):** изначально включили `Bot Fight Mode = ON` по рекомендации — выяснилось, что он триггерит JS-challenge даже для curl/Googlebot UA → категоризаторы получали 403. **Отключили** Bot Fight Mode + Security Level → `Essentially Off`. Для будущих доменов под VPN-инфру **НЕ включать Bot Fight Mode**, иначе сломается категоризация.

**Acceptance (выполнено):**
- `dig qubexlabs.com NS +short` → `marjory.ns.cloudflare.com`, `bryce.ns.cloudflare.com` ✅
- `curl -sI https://qubexlabs.com` → `HTTP/2 200`, `cf-cache-status: HIT` ✅
- `<title>Qubex Labs — Distributed Observability Research</title>` отдаётся ✅
- Whois показывает Namecheap WhoisGuard, не личные данные ✅
- TTL 215 + DNS-only — отложено до Stage 4 (создание `w1`-записи)

### Stage 3 — RealiTLScanner на текущих VPS (разведка) — ✅ ГОТОВО для fi02 и ru01 (2026-05-10)

Цель: получить пулы по 4 donor SNI per VPS. Деплой не трогаем — это разведка.

**Что сделано (2026-05-10):**
- Запустил `RealiTLScanner` на `fi02` (NL, /24=204.168.248.0/24) и `ru01` (RU AS48282 vdsina, /24=91.184.245.0/24). Скан занял ~25 секунд каждый, дал 22+24 кандидата.
- Curl-валидация TLS handshake через `--resolve` для каждого кандидата (HTTP/2 ответ, валидный cert, не CDN-origin).
- Отбраковка вручную (VPN/gambling/dating/CDN-origin/fake-cert/Kubernetes-default) → отобрано 4 SNI per нода.
- Результат: [`docs/research/sni-pools.md`](../research/sni-pools.md) (полные пулы + обоснование + резерв).
- Raw CSV-логи сканов: [`docs/research/sni-scan-raw/`](../research/sni-scan-raw/).

**Команда сканирования (актуальная для текущей версии RealiTLScanner — флаг `-showFail` убран в новой версии, заменён на `-out CSV` + log в stderr):**
```bash
ssh root@<VPS> bash -c '
  cd /tmp && \
  wget -q https://github.com/XTLS/RealiTLScanner/releases/latest/download/RealiTLScanner-linux-64 && \
  chmod +x RealiTLScanner-linux-64 && \
  nohup ./RealiTLScanner-linux-64 -addr <SUBNET>/24 -port 443 -thread 30 -timeout 8 -out scan.csv > scan.log 2>&1 &
'
# подождать 30 сек, потом scp scan.csv
```

**Подобранные пулы (см. sni-pools.md для деталей):**
- `fi02` (NL): dest=`creative-demo.dh.sg`, serverNames=[creative-demo.dh.sg, crm.legalexito.com, ekizenergy.tech, mail.mxhosting.org]
- `ru01` (RU): dest=`grishchenkov.ru`, serverNames=[grishchenkov.ru, m.vk.com, www.max.ru, mail.hohlov.tech] — главное прикрытие `m.vk.com` + `www.max.ru` (белые у RKN)

**Не сделано:**
- `nl01` (146.103.112.91) — port 22 timeout с openclaw-workspace, отложено. Уточнить статус ноды.
- MWS RU-VPS — ждёт Stage 4 (нода ещё не создана). При создании запустить тот же скан, отдельный приоритет — есть ли в /24 MWS `*.x5.ru` (proven у `geodataload.com`).

**Acceptance (выполнено):**
- ✅ Подобраны 4 SNI per VPS с обоснованием (см. `sni-pools.md`)
- ✅ Raw CSV-логи сохранены в репе

### Stage 4 — MWS RU-VPS как новая нода `[LTE 2]` — ~3 часа

Цель: реальная нода в РФ под `vless-reality-rusni-in`.

- [ ] **Подготовка VPS** (доступ дать через ssh-key):
  ```bash
  bash deploy/scripts/prepare-vps.sh
  ```
  ⚠️ Сначала запатчить ip6tables-блок (TODO из 2026-05-07, см. `memory/2026-05-07.md`)

- [ ] **Сгенерить новые Reality keys** (НЕ переиспользовать с Falkenstein):
  ```bash
  bash deploy/scripts/deploy-xray-new.sh
  ```

- [ ] **RealiTLScanner на MWS** (Stage 3 для самой ноды) — найти 4 SNI в подсети MWS (вероятно, корпсайты на AS39497).
  - Первичный (`dest`) — оставить как `ads.x5.ru` (проверен конкурентом, гарантированно работает на RU-маршруте) — **если** он есть в подсети MWS, иначе берём верхний из сканера.
  - 3 остальных — из RealiTLScanner.

- [ ] **Конфиг Xray** (`/opt/xray/config.json`):
  ```json
  {
    "inbounds": [{
      "port": 1443,
      "protocol": "vless",
      "tag": "vless-reality-rusni-in",
      "settings": { "decryption": "none", "clients": [] },
      "streamSettings": {
        "network": "tcp",
        "security": "reality",
        "realitySettings": {
          "show": false,
          "dest": "<sni-1>:443",
          "xver": 0,
          "serverNames": ["<sni-1>", "<sni-2>", "<sni-3>", "<sni-4>"],
          "privateKey": "<новый_per_server>",
          "shortIds": ["<random_8byte_hex>", ""]
        }
      }
    }]
  }
  ```

- [ ] **A-record на новом домене:** `w1.qubexlabs.com → <MWS-IP>` (DNS-only, TTL 215)

- [ ] **INSERT в БД:**
  ```sql
  INSERT INTO vpn_servers (
    name, country_code, host, port,
    public_key, short_id, server_names,
    inbound_tag, xray_api_host, xray_api_port,
    priority, is_active
  ) VALUES (
    '[LTE 2] Россия (мобильный)',
    'RU',
    'w1.qubexlabs.com',             -- ВАЖНО: домен, не IP
    1443,
    '<новый_публичный_ключ>',
    '<short_id>',
    '["<sni-1>","<sni-2>","<sni-3>","<sni-4>"]'::jsonb,  -- после миграции 006
    'vless-reality-rusni-in',
    '<MWS-IP>',                      -- xray-api по прямому IP (не через домен)
    10085,
    5,                               -- выше id=144 (=10), идёт первым в подписке
    true
  );
  ```

- [ ] **ResyncServer:**
  ```bash
  docker run --rm --network vpn-stack_vpn fullstorydev/grpcurl \
    -plaintext -d '{"server_id":<новый_id>}' \
    vpn-service:50062 vpn.v1.VPNService/ResyncServer
  ```

- [ ] **Smoke-тест:**
  - `curl /api/v1/subscription/<token>` → видно профиль `📶 [LTE 2] Россия (мобильный)`
  - SNI в ссылке отличается между запросами (Stage 1 random)
  - Хост резолвится в IP MWS

- [ ] **Реальный тест:** прислать ссылку Тапдыгу (или другому LTE-юзеру в проблемном регионе), подтвердить что обход работает.

**Acceptance:** юзер из Хакасии подтверждает «работает», статистика подключений на новой ноде растёт.

### Stage 5 — Применить мульти-SNI на текущие ноды — ~1 час

Цель: убрать `apple.com` со старых VPS, накатить пулы из Stage 3.

Для каждого существующего VPS:
- [ ] **Шаг A:** добавить 4 новых SNI В ДОПОЛНЕНИЕ к `apple.com`:
  ```json
  "serverNames": ["apple.com", "<new-1>", "<new-2>", "<new-3>", "<new-4>"]
  ```
  Старые клиенты с `apple.com` продолжают работать.
- [ ] `UPDATE vpn_servers SET server_names = $1::jsonb WHERE id = $2;`
- [ ] `docker restart xray` + `ResyncServer`
- [ ] **Шаг B (через 1-2 недели):** убрать `apple.com`, оставить только 4 новых:
  ```json
  "serverNames": ["<new-1>", "<new-2>", "<new-3>", "<new-4>"]
  ```
  ⚠️ `dest = serverNames[0]:443` обязательно.

**Acceptance:** в логах Xray больше нет warning про apple.com, новые юзеры получают рандомный SNI из пула.

### Stage 6 — Hot-spare Selectel — отложено до first failure

Цель: backup-нода на случай бана MWS / RKN-блока его IP.

- [ ] Купить минимальный VPS на Selectel (~250 ₽/мес, физ.лицо ОК)
- [ ] Развернуть тот же inbound `vless-reality-rusni-in` (тот же шаблон что MWS)
- [ ] **Свои** Reality keys (отдельные от MWS!)
- [ ] Добавить A-record `w1.qubexlabs.com → <Selectel-IP>` рядом с MWS (round-robin)
- [ ] **Suspended state** до first failure — не платим за активный

**Acceptance:** есть готовый скрипт миграции, который активирует Selectel за 5 минут при бане MWS.

---

## ⚠️ Жёсткие правила

1. **`dest = serverNames[0]:443`** — иначе Reality handshake падает (см. task 13)
2. **Per-server Reality keys** — генерим заново на каждом новом VPS, НЕ копируем (см. ошибку конкурента)
3. **Domain в `vpn_servers.host`, не IP** — иначе ротация A-record бесполезна
4. **Cloudflare DNS-only** для VPN-нод (не Proxy!) — Reality несовместим с CF Proxy на :443
5. **TTL 215с** — быстрая ротация без апдейта подписок у юзеров
6. **VPN-домен ≠ control-plane домен** — `osmonai.com` не светим в VPN-нодах
7. **Не подменять `serverName` в client config** — у конкурента это слабое место (`aviasales.com` при `dest=ads.x5.ru`), DPI рано или поздно поймает несоответствие

---

## 💸 Бюджет

| Статья | Разово | Регулярно |
|---|---|---|
| Домен `qubexlabs.com` (Namecheap) | ~$11 (~1000₽) | ~$11/год |
| Stub-лендинг (Cloudflare Pages) | 0 | 0 |
| MWS Cloud VPS | 0 (уже есть) | 0 |
| Selectel hot-spare (suspended) | 0 | ~50₽/мес |
| Cloudflare DNS | 0 | 0 |
| **Итого** | **~1000₽** | **~$11/год + ₽50/мес** при активации Selectel |

При успехе (Хакасия и проблемные регионы конвертятся) — окупается с **~3 платных юзеров** из этих регионов.

---

## ✅ Acceptance Criteria (полный)

- [ ] Stage 1: dev-стенд эмитит подписку с РАНДОМНЫМ SNI из массива
- [ ] Stage 2: `qubexlabs.com` на CF, anon WHOIS, 2FA, TTL=215, stub-лендинг отдаётся (домен куплен ✅)
- [ ] Stage 3: SNI-пулы по 4 шт на каждый VPS зафиксированы в `research/sni-pools.md`
- [ ] Stage 4: MWS-нода в БД, ResyncServer прошёл, профиль `[LTE 2]` виден в подписке
- [ ] Stage 4: реальный юзер из проблемного региона подтвердил «работает»
- [ ] Stage 5A: на старых VPS добавлены новые SNI рядом с `apple.com`
- [ ] Stage 5B: через 1-2 недели `apple.com` убран
- [ ] Stage 6: Selectel hot-spare готов как failover

---

## 🔥 Risk-matrix после реализации

| Угроза | До | После |
|---|---|---|
| Мобильный DPI режет иностранные IP-pool | LTE-юзеры в Хакасии не работают | RU-IP MWS, обходит |
| ТСПУ ловит «один SNI на VPS» | паттерн `apple.com` × тысячи коннектов | 4 SNI, random per юзер |
| Apple-домены в чёрном списке Reality | Xray warning, потенциальный block | реальные соседи VPS |
| RKN добавил IP в реестр | юзеры теряют доступ до апдейта подписки | A-record меняется за 215с |
| RKN положил весь домен | всё лежит | hot-spare домен (Stage 2 + Selectel) |
| Утёк Reality key с одного VPS | компромисс ВСЕХ нод (как у конкурента) | per-server keys, остальные живы |
| RKN/CF положили `osmonai.com` | VPN-ноды лежат с подпиской | VPN-домен независим |

---

## 📅 Когда что делать

- **Stage 1, 3** — можно начинать **сейчас** (код + разведка, ничего не ломает)
- **Stage 2** — после Stage 1 (нужен новый домен для Stage 4)
- **Stage 4** — когда aziz даст ssh-доступ к MWS
- **Stage 5** — после успеха Stage 4 (накатываем подобранные SNI на старые ноды)
- **Stage 6** — при first failure MWS (или превентивно если время есть)

---

## 🗺 Связанные

- [12-sni-rotation.md](./12-sni-rotation.md) — детали миграции 006 + код vpn-service
- [13-realitlscanner-donors.md](./13-realitlscanner-donors.md) — детали RealiTLScanner
- [17-ru-vps-lte.md](./17-ru-vps-lte.md) — анализ инфры geodataload.com
- [16-rkn-resilience.md](./16-rkn-resilience.md) — RU-mirror подписки (✅ done)
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/services/vpn-service/internal/model/vpn.go" />
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/services/vpn-service/internal/repository/vpn.go" />
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/services/vpn-service/internal/service/vpn.go" />
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/deploy/scripts/deploy-xray-new.sh" />
- <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/deploy/scripts/prepare-vps.sh" />
