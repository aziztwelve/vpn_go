# 20. Whitelisted RU-VPS для IP+SNI комбо-фильтра

**Дата:** 2026-05-13
**Статус:** 🟡 Обсуждение — собрать данные и принять решение по хостеру
**Автор:** Devin + aziz
**Источник:** [hxehex/russia-mobile-internet-whitelist](https://github.com/hxehex/russia-mobile-internet-whitelist)
(3.2k★, регулярно обновляется community)

**Связано:**
- [17-ru-vps-lte.md](./17-ru-vps-lte.md) — предыдущий заход (Yandex.Cloud) —
  устарел: TSPU фильтрует `AS200350` (Yandex.Cloud) отдельно от `AS13238`
  (Yandex основной), YC уже блокируется в ряде регионов.
- [end_sni.md](./end_sni.md) — Stage 4 (tw1 NSK) и Stage 6 (hot-spare Selectel)
  — продолжение этой задачи в общем плане SNI/RU-инфры.
- [12-sni-rotation.md](./12-sni-rotation.md) — массив SNI per inbound (уже в проде).
- `../vpn/lte.md` — текущая LTE-архитектура (Hetzner FIN + tw1 NSK cascade).
- Memory: <ref_file file="/root/.openclaw/workspace/memory/2026-05-13.md" /> —
  полный анализ AS-vs-whitelist + конкретные whitelisted /24 блоки топ-хостеров.

---

## 🎯 Цель

Поднять RU-VPS на хостере, чьи **IP в whitelist'е мобильных операторов РФ**
(по `cidrwhitelist.txt` / `ipwhitelist.txt` из репо), и **мигрировать туда
cascade-relay** с tw1 (TimeWeb NSK) — чтобы трафик RU LTE-юзеров проходил
через whitelisted IP даже у операторов которые перешли на **`IP + SNI`-комбо-
фильтр** (МТС/МегаФон в Хакасии, Красноярском крае, ХМАО, по фидбеку).

### Зачем сейчас (постановка)

По состоянию на 2026-05-13 у нас:

- `id=172 [LTE] Мобильный интернет`: `host=w1.qubexlabs.com:1443` → tw1 NSK
  (`186.246.31.92`, TimeWeb AS9123) → cascade на fin2 (`178.105.1.202`,
  Hetzner Falkenstein, AS24940).
- Stage 1 quick-fix от 2026-05-13 ~05:00 UTC: `serverNames` на fin2 = `[web.max.ru,
  alfabank.ru, ads.x5.ru]`, БД `server_names = [web.max.ru, alfabank.ru]`,
  `dest = web.max.ru:443`. `web.max.ru` ✅ в `whitelist.txt`.
- **Сейчас работает** для юзеров у которых оператор **SNI-only** (видим живые
  reality-accepted с RU LTE IP вроде `188.162.64.73`).
- **Не работает** для юзеров с **IP+SNI комбо-фильтром**: TCP-connect к
  `186.246.31.92:1443` (TimeWeb NSK) у них не проходит даже при правильном SNI,
  потому что **TimeWeb AS9123 не в whitelist** оператора (по 34/848 префиксов
  только в `cidrwhitelist.txt`, и `186.246.31.0/24` в эти 34 не попал — см.
  memory от 2026-05-13).

Чтобы покрыть **обе** группы юзеров, нужен RU-VPS с IP **из конкретного
whitelisted-блока** одного из крупных хостеров.

---

## 📚 Контекст: что выяснили на 2026-05-13

Распарсили `cidrwhitelist.txt` (30 189 CIDR-блоков, актуально на момент сегодня),
прогнали через RIPE Stat API список из 36 ASN известных RU/EU VPS-хостеров.
**Таблица кандидатов** (см. memory 2026-05-13 для полной):

| ASN | Хостер | v4 префиксы | WL hits | WL % | Доступ |
|---|---|---:|---:|---:|---|
| AS47764 | **Cloud.ru** (VK / Mail.ru) | 93 | 48 | **52%** | KYC юрлицо/паспорт |
| AS50340 | **Selectel-2** | 417 | 75 | **18%** | паспорт, можно ТП-тикетом просить IP |
| AS49505 | **Selectel** | 995 | 114 | **12%** | то же |
| AS198610 | **Beget** | 499 | 42 | **8%** | паспорт, без выбора IP |
| AS197695 | **Reg.ru / RUVDS** | 437 | 28 | **6%** | паспорт |
| AS9123 | **TimeWeb** (наш) | 848 | 34 | **4%** | уже клиент, можно попробовать тикетом |
| AS210644 | **Aeza** | 315 | 8 | **2.5%** | паспорт, абуз-friendly |
| AS200350 | ❌ **Yandex Cloud** | 50 | 21 | 42% | **TSPU режет AS отдельно, не подходит** |

Конкретные whitelisted /24-блоки топ-хостеров (выгружены через RIPE):

```
Cloud.ru (AS47764, 48 префиксов):  5.188.140.0/22, 45.84.128.0/22,
  185.100.104.0/22, 90.156.148.0/22, 5.101.40.0/22, 45.136.20.0/22,
  91.231.134.0/24, 89.208.228.0/22, 89.221.235.0/24, 95.163.216.0/22,
  185.241.192.0/22, 185.16.244.0/22, 188.93.56.0/21, 91.231.133.0/24,
  5.61.16.0/21, 90.156.232.0/21, 91.231.132.0/22, 185.16.247.0/24,
  178.22.88.0/21, 128.140.168.0/21, 193.203.40.0/22, 217.69.128.0/20,
  5.61.232.0/21, 217.20.144.0/20, 83.222.28.0/22, … (+23)

Selectel (AS49505, 114 префиксов):  5.188.158.0/23, 31.184.224.0/22,
  31.184.215.0/24, 5.188.40.0/24, 185.232.67.0/24, 31.184.220.0/22,
  5.188.119.0/24, 82.202.222.0/24, 82.202.233.0/24, 88.218.56.0/22,
  188.68.200.0/21, 82.202.210.0/24, 185.11.72.0/22, 5.188.158.0/24,
  82.202.211.0/24, 82.202.239.0/24, 5.188.118.0/23, 5.8.77.0/24,
  5.188.42.0/24, 188.68.203.0/24, 82.202.249.0/24, 176.222.56.0/24,
  31.184.219.0/24, 77.223.114.0/24, 195.19.168.0/22, … (+89)

Selectel-2 (AS50340, 75 префиксов):  88.218.59.0/24, 5.188.72.0/21,
  109.71.8.0/21, 194.164.245.0/24, 185.55.56.0/22, 82.202.237.0/24,
  82.202.226.0/24, 109.71.12.0/23, 185.108.18.0/24, 82.202.213.0/24,
  … (+65)

Beget (AS198610, 42 префикса):  217.114.0.0/21, 217.26.24.0/21,
  185.225.32.0/22, 5.101.152-159.0/24, 194.156.116.0/22, 31.128.37.0/24,
  185.78.30-31.0/24, 93.92.80.0/24, … (+17)

TimeWeb (AS9123, 34 префикса):  5.188.207.0/24, 92.118.115.0/24,
  176.57.214.0/24, … (+31) — наш `186.246.31.92` НЕ в этом списке.
```

Способ ground-truth проверки (из README репо и Habr 990206):
**деплоим nginx на :443 с self-signed cert, открываем `https://<IP>/`
с RU LTE-сим без VPN**. Если страница загружается — IP whitelisted у
этого оператора. Если timeout / connection reset — нет.

Whitelisting **варьируется per-оператор / регион / вышка**, поэтому
итоговая проверка нужна **с реального RU LTE** в нескольких регионах
(минимум: МТС Москва, МегаФон Хакасия — если есть тестер).

---

## 🏗 Архитектура (целевая)

```
[ юзер LTE, оператор с IP+SNI вайт-листом, регион Хакасия/Красноярск ]
         │
         │ TCP :1443  (DNS RR через CF: w1.qubexlabs.com → IP1, IP2, ...)
         ▼
  ┌──────────────────────────────────────────────────────────────┐
  │ A-records w1.qubexlabs.com (DNS-only, TTL=215):              │
  │  • <NEW-IP>      Selectel / Cloud.ru — whitelisted /24        │
  │  • 186.246.31.92 tw1 TimeWeb NSK                              │
  │    (оставляем как fallback, понизив priority)                 │
  └──────────────────────────────────────────────────────────────┘
         │
         │ VLESS Reality :1443, SNI ∈ [web.max.ru, alfabank.ru]
         │ Reality keys = тот же pbk/sid что на fin2 (cascade-style)
         ▼
   [ NEW-RU-VPS dokodemo-door :1443 → ]
         │
         │ TCP → :1443 fin2 (Hetzner Falkenstein, exit-node)
         ▼
   [ fin2 vless-reality-rusni-in :1443 ]  ──►  freedom  ──►  интернет
```

`pickSNI(srv)` уже работает (см. `subscription_config.go:556`), даст рандомный
SNI из `server_names` массива per VLESS-link.

---

## 📋 Этапы (последовательно)

### Stage 1 — Выбор хостера и IP (researcher: Devin / aziz, ~1 день)

- [ ] **Финальный шорт-лист** из таблицы выше: 2-3 кандидата с учётом цены,
  KYC, регламента «выбрать IP из подсети». Предварительно:
  - **Selectel** (best для гибкости: 189 whitelisted-префиксов суммарно,
    тикет «дайте IP из X.X.X.0/24» обычно отрабатывает за час, минимум
    ~500₽/мес).
  - **Beget** (дёшево ~150₽/мес, но IP-блок назначается случайно — лотерея,
    может потребоваться 2-3 reset'а; 42 whitelisted-/24).
  - **Cloud.ru** (max % whitelisted, но KYC юрлица сложнее, минимум $10-20/мес;
    оставить на second-pass если первые два не зайдут).
- [ ] **Подача заявки** на VPS:
  - Минимум: 1 vCPU, 1GB RAM, 10GB SSD, 1 IPv4, **РФ-локация Москва**.
  - В тикет указать: «нужен IP из подсети `<выбранный whitelisted /24>`, см.
    список выше». Если хостер не даёт выбирать — фиксируем выделенный IP и
    проверяем сами.
- [ ] **Ground-truth проверка whitelist'a**:
  ```bash
  # На свежем VPS:
  apt install -y nginx && systemctl start nginx
  # Проверяем что :443 открыт и отдаёт self-signed:
  curl -kI https://<NEW-IP>/
  ```
  Затем **с RU LTE-сим без VPN** открыть `https://<NEW-IP>/` — если страница
  грузится (или хотя бы TLS handshake проходит) — IP whitelisted. Проверка
  желательна с 2-3 разных операторов (МТС/Билайн/МегаФон).
- [ ] Если IP не прошёл — повторно тикетом / reset IP / следующий хостер.

### Stage 2 — Подготовка VPS как cascade-relay (~30 мин)

- [ ] `add_server/prepare-relay-vps.sh` — уже есть, ставит docker + iptables.
  Запустить:
  ```bash
  ssh root@<NEW-IP> 'bash -s' < add_server/prepare-relay-vps.sh
  ```
- [ ] Развернуть xray с `dokodemo-door :1443 → 178.105.1.202:1443`:
  - Скопировать паттерн из `/opt/xray/config.json` на текущем tw1
    (`186.246.31.92`) — там готовый dokodemo-конфиг.
  - `docker run -d --name xray --restart unless-stopped -v
    /opt/xray:/etc/xray -p 1443:1443 teddysun/xray:latest`.
- [ ] Открыть `:1443` в iptables для всех. `:10085` (xray api) не нужен,
  т.к. xray api управляется на стороне exit-node (fin2).

### Stage 3 — Включение в DNS и подписку (~10 мин)

Вариант A — **миграция (риск):** перезаписать `w1.qubexlabs.com` A-record
с `186.246.31.92` (tw1) на `<NEW-IP>`. Через TTL 215 все юзеры начнут
коннектиться на новый IP. Старый tw1 можно держать выключенным.

Вариант B — **safer (round-robin):** добавить **второй** A-record рядом со
старым. Cloudflare round-robin раскинет нагрузку; те у кого старый tw1
не работал — попадут на новый и взлетят. Через 1-2 недели снять старый.

- [ ] Реализуем Вариант B как минимум на старте.
- [ ] **БД не трогаем** — `host=w1.qubexlabs.com` уже DNS-резолвится в нужный
  пул. Ключи и SNI у нового VPS = те же что на fin2 (cascade pattern).

### Stage 4 — Verify (~30 мин — 1 ч)

- [ ] xray-логи на fin2: проверяем что accepted-коннекты приходят с
  source-IP `<NEW-IP>` (а не только с tw1 `186.246.31.92`).
- [ ] `traffic_samples` по `id=172` — рост за 2-4 часа.
- [ ] (если есть RU LTE-тестер) — реальное подключение через приложение
  HAPP, открытие youtube.com / instagram.com из проблемного региона.

### Stage 5 — Decom tw1 или сохранить как fallback (~ через 1-2 недели)

- [ ] Если whitelisted-IP стабильно работает у всех проблемных юзеров —
  снять `186.246.31.92` из DNS, оставить только новый IP.
- [ ] Tw1 контейнер выключить, ssh-доступ оставить для возможного rollback'а.
  Через месяц без алертов — удалить VPS на TimeWeb.

---

## ⚠️ Риски / нюансы

- **Whitelist репо обновляется**: список IP/CIDR актуален «на сейчас».
  Operator'ы могут изменять whitelist раз в недели. План: при выборе
  VPS-IP сразу подписаться на репо watch / pull обновлений в CI.
- **IP в whitelist ≠ IP всегда работает у всех операторов**. Whitelist
  оператора `X` ⊃ whitelist оператора `Y`. Конкретный IP может быть в
  МТС-листе но не в МегаФон. Финальная проверка — реальные тестеры.
- **«Shield» эффект**: некоторые юзеры всегда работают (см. README репо),
  это flag в системе оператора. Их статистика не репрезентативна.
- **DC-to-DC traffic пока не whitelist-фильтруется** ([neversleeps.moscow](https://neversleeps.moscow/publications/vpn_complexity.html)),
  поэтому cascade-pattern (relay → exit за рубежом) остаётся актуальным.
- **TimeWeb наш текущий tw1** уже в проде — можно сначала **попробовать
  тикетом сменить IP** на whitelisted /24 из 34 префиксов TimeWeb. Это
  дешевле/быстрее чем подключать нового хостера. Но шанс что выдадут
  именно этот блок — низкий.
- **Cloud.ru / VK Cloud юрлицо-KYC**: если у aziz есть готовое ИП/ООО
  — это лучший хостер по wl%, но процесс регистрации долгий (3-7 дней).

---

## 💸 Бюджет (оценка)

| Хостер | Конфиг | ₽/мес | Notes |
|---|---|---:|---|
| Beget | 1 vCPU, 1GB RAM, 10GB SSD | ~150-200 | дешевле всех, лотерея на IP |
| Selectel | 1 vCPU, 1GB RAM, 10GB SSD | ~500 | best по гибкости IP-выбора |
| RUVDS | 1 vCPU, 1GB RAM, 10GB SSD | ~250 | средний вариант |
| Cloud.ru | shared CPU, 1GB RAM | ~700-1500 | плати-за-час, гибко но дорого |

Первый месяц — Selectel или Beget. Если работает — продолжаем. Если IP не
whitelisted — перезаказываем у того же хостера или меняем.

---

## ✅ Acceptance Criteria

- [ ] Stage 1: выбран хостер, заказан VPS, получен IP, IP подтверждён в
  одном из `cidrwhitelist.txt` блоков по AS+RIPE.
- [ ] Stage 1: ground-truth проверка пройдена — `https://<NEW-IP>/` грузится
  с RU LTE-сим **минимум одного оператора** в проблемном регионе.
- [ ] Stage 2: relay-VPS развёрнут, xray dokodemo→fin2 запущен, `:1443`
  открыт, TLS-probe из РФ показывает корректный fin2-cert.
- [ ] Stage 3: в DNS `w1.qubexlabs.com` появилась запись на новый IP
  (вариант A или B).
- [ ] Stage 4: на fin2 в xray-логах накопилось ≥10 accepted-коннектов
  с source-IP = `<NEW-IP>`.
- [ ] Stage 4: traffic_samples по `id=172` за 24ч после миграции > 100MB
  (≥3× от текущих ~2.3MB/24h).
- [ ] (Stretch) Stage 5: подтверждение хотя бы одного юзера из проблемного
  региона (Хакасия) что «теперь работает». Известный кандидат — `@Tapdyg1`
  (см. задачу 17).

---

## 📂 Артефакты исследования (2026-05-13)

- Whitelist скачан: `/tmp/rmiw/{whitelist.txt,ipwhitelist.txt,cidrwhitelist.txt}`
- Скрипт «AS vs whitelist matrix»: `/tmp/find_whitelisted_hosters.py`
- Скрипт «whitelisted /24 prefixes per AS»: `/tmp/show_hits.py`
- Скрипт «check IP in cidrwhitelist»: `/tmp/check_cidr.py`
- Полный анализ: <ref_file file="/root/.openclaw/workspace/memory/2026-05-13.md" />

Скрипты эфемерные (в `/tmp`); при следующем заходе скачать заново
(см. URL в скриптах).

---

## 🔗 Внешние источники

- [hxehex/russia-mobile-internet-whitelist](https://github.com/hxehex/russia-mobile-internet-whitelist)
  — главный источник whitelist'а, 3.2k★, regular updates.
- [escapingworm/russia-whitelist](https://github.com/escapingworm/russia-whitelist)
  — альтернативный лист, проверен на МТС.
- [Habr 990206](https://habr.com/en/articles/990206/) — практический гайд
  по обходу whitelist'ов + рекомендации хостеров. Упоминает hynet.space,
  Amnezia, Voxiproxy как ready-made альтернативы.
- [Sergei-thinker/vpn-setup](https://github.com/Sergei-thinker/vpn-setup) —
  README с пояснением почему YC выпал из дефолтной архитектуры (TSPU
  фильтрует AS200350 отдельно).
- [neversleeps.moscow VPN-complexity](https://neversleeps.moscow/publications/vpn_complexity.html)
  — общий обзор state-of-the-art censorship-bypass в РФ 2026.
