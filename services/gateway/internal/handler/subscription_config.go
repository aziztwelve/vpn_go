package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vpn/gateway/internal/client"
	pb "github.com/vpn/shared/pkg/proto/vpn/v1"
	"go.uber.org/zap"
)

// SubscriptionConfigHandler отдаёт публичную подписку клиентам Xray/V2Ray.
// URL: `GET /api/v1/subscription/{token}` (без JWT).
//
// Два формата:
//   - base64 (default): plain-text, base64-encoded список VLESS-ссылок.
//     Понимают все клиенты (Happ, V2RayNG, Hiddify, Streisand, v2rayN).
//   - json (`?format=json`): HAPP-специфичный массив полных Xray-конфигов.
//     Каждый элемент — отдельный "сервер" в списке HAPP со своим remarks и routing.
//
// Токен — 48 hex-символов из `vpn_users.subscription_token` (миграция 005).
// Валидация: токен должен существовать и указывать на vpn_user с активной
// подпиской (`status IN ('active','trial') AND expires_at > NOW()`).
type SubscriptionConfigHandler struct {
	vpnClient *client.VPNClient
	logger    *zap.Logger
	// defaultCountry — ISO-2 код страны (UPPERCASE) для «дефолтного» сервера
	// в трёх режимах (⚡/🚀/🎬). Если в активных есть сервер с таким
	// country_code — он используется для этих ссылок (вне зависимости от
	// load_percent). Если такого сервера нет — fallback на servers[0]
	// (наименее загруженный). Полный список стран в подписке не режется.
	defaultCountry string
}

func NewSubscriptionConfigHandler(vpnClient *client.VPNClient, defaultCountry string, logger *zap.Logger) *SubscriptionConfigHandler {
	return &SubscriptionConfigHandler{
		vpnClient:      vpnClient,
		logger:         logger,
		defaultCountry: strings.ToUpper(strings.TrimSpace(defaultCountry)),
	}
}

// SubscriptionConfig — точка входа HTTP.
// URL: `GET /api/v1/subscription/{token}`.
// Формат выбирается по UA и query (?format=json|base64), см. serve().
func (h *SubscriptionConfigHandler) SubscriptionConfig(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, false)
}

// SubscriptionConfigTest — идентичная ручка, но ВСЕГДА отдаёт base64
// (VLESS-URI'ы), без UA-sniff. URL: `GET /api/v1/subscription-test/{token}`.
// Создан для A/B-проверки клиентов, которые не едят JSON-формат (см. Happ
// macOS — у него baseline, а JSON валится на некоторых билдах).
func (h *SubscriptionConfigHandler) SubscriptionConfigTest(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, true)
}

// serve — общая логика обеих ручек: валидация токена, device-touch,
// заголовки, выбор формата. forceBase64=true → всегда base64, независимо
// от UA и query. forceBase64=false → старая логика (UA-sniff + ?format=).
func (h *SubscriptionConfigHandler) serve(w http.ResponseWriter, r *http.Request, forceBase64 bool) {
	token := chi.URLParam(r, "token")
	if token == "" {
		writeJSONError(w, http.StatusBadRequest, "token required")
		return
	}

	cfg, err := h.vpnClient.GetSubscriptionConfig(r.Context(), token)
	if err != nil {
		// gRPC NotFound → HTTP 404. Всё остальное → 500.
		// Код NotFound летит из vpn-service когда токен неизвестен или
		// подписка истекла. Клиент Happ на 404 показывает "подписка недоступна".
		h.logger.Info("subscription lookup failed",
			zap.String("token_prefix", safePrefix(token)),
			zap.Error(err))
		if isGRPCNotFound(err) {
			writeJSONError(w, http.StatusNotFound, "subscription not found or expired")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if len(cfg.GetServers()) == 0 {
		// Редкий кейс: подписка жива, но в БД нет is_active серверов.
		// Возвращаем валидный пустой ответ — клиент покажет "нет серверов".
		h.logger.Warn("active subscription but no active servers",
			zap.String("token_prefix", safePrefix(token)))
	}

	// Best-effort device-touch: фиксируем "клиент-приложение тянуло
	// подписку". Делаем в горутине, чтобы не задерживать ответ Xray-клиенту.
	// Ошибки только логируем — UI оживёт на следующем GET /vpn/connections.
	// Свой context (не r.Context()) — иначе он отменится сразу после ответа.
	userAgent := r.Header.Get("User-Agent")
	go func(tok, ua string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := h.vpnClient.RegisterDeviceTouch(ctx, tok, ua)
		if err != nil {
			h.logger.Debug("device touch failed",
				zap.String("token_prefix", safePrefix(tok)),
				zap.String("ua", ua),
				zap.Error(err),
			)
			return
		}
		if resp.GetCreated() {
			h.logger.Info("device touch registered",
				zap.String("token_prefix", safePrefix(tok)),
				zap.String("device", resp.GetDeviceIdentifier()),
			)
		}
	}(token, userAgent)

	// HAPP/v2rayN/Hiddify distinguish legitimate VPN subscriptions from generic
	// text by a set of well-known headers. Без них клиент пытается распарсить
	// тело как unknown формат и может упасть на Xray-core с ошибкой типа
	// "Invalid integer range". Референс — extravpn.info, marzban и т.п.
	expireUnix := parseRFC3339(cfg.GetExpiresAt()).Unix()
	userInfo := fmt.Sprintf("upload=0; download=0; total=0; expire=%d", expireUnix)

	// profile-title закодирован в base64 по HAPP-конвенции.
	profileTitle := "base64:" + base64.StdEncoding.EncodeToString([]byte("MaydaVpn"))
	// filename используется клиентом как id подписки локально (нужен для
	// корректного обновления — чтобы клиент не создавал дубликат).
	// ASCII-only, без …/emoji — старые клиенты парсят HTTP-header строго.
	filename := "sub-" + tokenPrefix(token, 8)

	w.Header().Set("Profile-Update-Interval", "1")
	w.Header().Set("Subscription-Userinfo", userInfo)
	w.Header().Set("Profile-Title", profileTitle)
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	w.Header().Set("Support-URL", "https://t.me/maydavpn_support")
	w.Header().Set("Profile-Web-Page-URL", "https://cdn.osmonai.com")
	// Happ всегда создаёт `freedom` outbound с тегом `fragment` — читает значения
	// из этих заголовков и подставляет в Xray config как `fragment.length/packets/interval`.
	// Xray требует ОДИН диапазон "start-end" (или одно число / "tlshello"), список
	// через запятую "10-30,100-200" приведёт к "Invalid integer range" при запуске.
	// Поэтому даём валидные для Xray single-range значения.
	w.Header().Set("Fragmentation-Enable", "1")
	w.Header().Set("Fragmentation-Length", "10-30")
	w.Header().Set("Fragmentation-Packets", "tlshello")
	w.Header().Set("Fragmentation-Interval", "10-30")
	w.Header().Set("Ping-Type", "proxy-head")
	w.Header().Set("Sub-Expire", "1")

	// Test-endpoint (/api/v1/subscription-test/...) форсит base64 независимо
	// от UA и query — так мы можем параллельно прогонять тот же токен через
	// оба формата и сравнивать поведение клиентов.
	if forceBase64 {
		writeBase64Format(w, cfg, h.defaultCountry)
		return
	}

	// Выбор формата:
	//   1. Явный `?format=json` — приоритет, отдаём JSON (HAPP-extension).
	//   2. Явный `?format=base64` — приоритет, отдаём base64 (тоже работает в HAPP).
	//   3. Без query: UA-sniff. HAPP шлёт UA вида `Happ/<ver>/<platform>/...`
	//      на всех платформах (iOS/Android/Linux/Windows). Он умеет JSON-формат
	//      и только в JSON виден `🌐 АВТО ВЫБОР` (balancer не выражается через
	//      VLESS-URI). Поэтому HAPP'у дефолтим JSON, всем остальным —
	//      универсальный base64.
	//
	// Это backwards-compat: ранее без query всегда был base64, теперь HAPP
	// при auto-update подписки сам начнёт получать обновлённый JSON-конфиг.
	switch r.URL.Query().Get("format") {
	case "json":
		writeJSONFormat(w, cfg, h.defaultCountry)
		return
	case "base64":
		writeBase64Format(w, cfg, h.defaultCountry)
		return
	}
	if strings.Contains(userAgent, "Happ/") {
		writeJSONFormat(w, cfg, h.defaultCountry)
		return
	}
	writeBase64Format(w, cfg, h.defaultCountry)
}

// writeBase64Format — стандартный subscription-формат:
// plain-text, base64(VLESS-ссылки по одной на строку).
//
// Структура списка в UI клиента:
//   1) ⚡ Обычный VPN            ← режим на «дефолтный» сервер (см. ниже)
//   2..M) Priority-серверы       ← {flag} {name} для серверов с priority>0
//                                  (например «🇩🇪 [LTE 1] Мобильный интернет»,
//                                   «🇫🇮 Finland (через РФ)») — сразу под
//                                  главным режимом, чтобы юзер из проблемной
//                                  сети не листал длинный список.
//   M+1) 🚀 Обход блокировок     ← на тот же дефолтный сервер
//   M+2) 🎬 YouTube без рекламы  ← на тот же дефолтный сервер
//   M+3..N) Обычные серверы      ← остальные {flag} {name} (priority=0),
//                                  отсортированные по load_percent
//
// Дефолтный сервер для трёх режимов выбирается так:
//  1. Если defaultCountry задан и в активных есть сервер с этим country_code
//     И priority=0 — берём его. Priority-серверы НЕ становятся дефолтом для
//     режимов: они — точечные опции, а не «универсальный VPN».
//  2. Иначе fallback на первый normal-сервер (priority=0); если их нет —
//     servers[0]. Репо отдаёт отсортированным по load_percent.
// Это даёт «закреплённую» страну для базового опыта: меньше сюрпризов когда
// у юзера несколько локаций и хочется чтобы режимы всегда стартовали с одной.
func writeBase64Format(w http.ResponseWriter, cfg *pb.GetSubscriptionConfigResponse, defaultCountry string) {
	servers := cfg.GetServers()
	user := cfg.GetVpnUser()

	priorityServers, normalServers := splitByPriority(servers)
	best := pickDefaultServer(servers, defaultCountry)

	var sb strings.Builder

	if best != nil {
		sb.WriteString(buildVLESSLink(user, best, profileRemark(profileFull, best)))
		sb.WriteByte('\n')
	}

	// Priority-блок (LTE / каскад / специальные обходы) — ПЕРЕД остальными
	// режимами. Юзер из проблемной сети сразу видит альтернативу.
	for _, srv := range priorityServers {
		sb.WriteString(buildVLESSLink(user, srv, serverRemark(srv)))
		sb.WriteByte('\n')
	}

	if best != nil {
		for _, p := range []routingProfile{profileBypass, profileYoutube} {
			sb.WriteString(buildVLESSLink(user, best, profileRemark(p, best)))
			sb.WriteByte('\n')
		}
	}

	for _, srv := range normalServers {
		sb.WriteString(buildVLESSLink(user, srv, serverRemark(srv)))
		sb.WriteByte('\n')
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(sb.String()))))
}

// splitByPriority разделяет список серверов на priority-блок и normal-блок.
//
//	priority >  0: priority-блок (вверху подписки, сразу после первого режима).
//	               Сортируется ASC: меньшее число выше (priority=1 над priority=2).
//	priority == 0: normal-блок, обычный порядок (load_percent, name из репо).
//	priority <  0: normal-блок, ПОНИЖЕНИЕ — ниже всех priority=0.
//	               Сортируется DESC: -1 выше -2 (более «отрицательное» — ниже).
//
// Применение priority<0: пометить устаревший/резервный сервер чтобы он не
// мозолил глаза вверху списка географий, но всё ещё был доступен.
//
// Stable-сортировка гарантирует, что при равных priority порядок из репо
// (ORDER BY load_percent, name) сохраняется.
func splitByPriority(servers []*pb.Server) (priority, normal []*pb.Server) {
	for _, s := range servers {
		if s.GetPriority() > 0 {
			priority = append(priority, s)
		} else {
			normal = append(normal, s)
		}
	}
	if len(priority) > 1 {
		sort.SliceStable(priority, func(i, j int) bool {
			return priority[i].GetPriority() < priority[j].GetPriority()
		})
	}
	if len(normal) > 1 {
		sort.SliceStable(normal, func(i, j int) bool {
			// Больший priority выше. priority=0 идут перед priority<0.
			return normal[i].GetPriority() > normal[j].GetPriority()
		})
	}
	return priority, normal
}

// buildVLESSLink — VLESS URI с уже готовым #remarks.
// Минимальный набор Reality-параметров совместимый с Xray-core и Happ/v2rayN/Hiddify:
//
//	encryption, type, security, flow, pbk, sid, sni, fp, spx.
//
// remarks передаётся целиком — вызывающий сам решает формат
// (например "⚡ Обычный VPN · 🇩🇪 Germany" или "🇩🇪 Germany").
func buildVLESSLink(user *pb.VPNUser, srv *pb.Server, remarks string) string {
	params := url.Values{}
	params.Set("encryption", "none")
	params.Set("type", "tcp")
	params.Set("security", "reality")
	params.Set("flow", user.GetFlow())
	params.Set("pbk", srv.GetPublicKey())
	params.Set("sid", srv.GetShortId())
	params.Set("sni", srv.GetServerNames())
	params.Set("fp", "chrome")
	params.Set("spx", "/")

	return fmt.Sprintf("vless://%s@%s:%d?%s#%s",
		user.GetUuid(),
		srv.GetHost(),
		srv.GetPort(),
		params.Encode(),
		url.QueryEscape(remarks),
	)
}

// profileRemark — формат remarks для режима: "⚡ Обычный VPN · 🇩🇪 Germany".
func profileRemark(p routingProfile, srv *pb.Server) string {
	return fmt.Sprintf("%s · %s %s", p.label(), flagEmoji(srv.GetCountryCode()), srv.GetName())
}

// serverRemark — формат remarks для выбора конкретного сервера: "🇩🇪 Germany".
func serverRemark(srv *pb.Server) string {
	return fmt.Sprintf("%s %s", flagEmoji(srv.GetCountryCode()), srv.GetName())
}

// writeJSONFormat — массив Xray-конфигов в том же порядке что base64-список:
//
//   1. ⚡ Обычный VPN · {flag} {default-country}    ← profileFull, дефолтный
//   2..M. {flag} {priority-server.name}             ← priority>0, сразу
//                                                     под главным режимом
//   M+1. 🚀 Обход блокировок · {flag} {default-country}
//   M+2. 🎬 YouTube без рекламы · {flag} {default-country}
//   M+3..N. {flag} {normal-server.name}             ← priority=0
//   N+1. 🌐 АВТО ВЫБОР                              ← если активных ≥2
//
// `⚡ Обычный VPN · {country}` и `{flag} {country}` (один из normal-серверов)
// дают дубликат outbound'а. Оставляем намеренно:
//   - первый — в группе режимов, как «универсальный VPN на дефолте»;
//   - второй — в группе выбора географии, как «весь трафик через эту страну».
// Юзеру это даёт привычный UX: сначала «как роутить», потом «через какую
// страну».
//
// Формат массива JSON-конфигов — HAPP-specific расширение subscription:
// клиент сам разбирает, добавляет каждый объект как сервер.
func writeJSONFormat(w http.ResponseWriter, cfg *pb.GetSubscriptionConfigResponse, defaultCountry string) {
	servers := cfg.GetServers()
	user := cfg.GetVpnUser()

	priorityServers, normalServers := splitByPriority(servers)
	best := pickDefaultServer(servers, defaultCountry)

	// 3 mode-конфига + N per-server + 1 auto.
	estimate := len(defaultProfiles) + len(servers)
	if len(servers) >= 2 {
		estimate++
	}
	configs := make([]map[string]interface{}, 0, estimate)

	if best != nil {
		configs = append(configs, buildXrayConfig(user, best, profileFull))
	}

	// Priority-серверы (LTE-обход / каскад) предназначены для проблемных
	// регионов — у юзеров там часто отказывают РУ-сервисы при походе
	// через зарубежный exit (антифрод банков, geo-блок Госуслуг). Поэтому
	// дефолтный routing-профиль для priority — split-tunnel (profileBypass):
	// RU-домены/IP направо, остальное — через VPN. Это совпадает с подходом
	// конкурентов (см. memory/2026-05-07.md, секция «анализ конкурента»).
	//
	// Дополнительно для LTE-style серверов (server_names в РУ-TLD) переопределяем
	// DNS на plain UDP `1.1.1.1`/`1.0.0.1` — DoH-эндпоинты (`cloudflare-dns.com:443`)
	// могут резаться DPI в whitelist-регионах. См. buildPlainDNS().
	// Каскадные priority-серверы (sni=apple.com) сохраняют DoH.
	for _, srv := range priorityServers {
		c := buildXrayConfig(user, srv, profileBypass)
		if isRussianSNI(srv.GetServerNames()) {
			c["dns"] = buildPlainDNS()
		}
		c["remarks"] = serverRemark(srv)
		configs = append(configs, c)
	}

	if best != nil {
		for _, p := range []routingProfile{profileBypass, profileYoutube} {
			configs = append(configs, buildXrayConfig(user, best, p))
		}
	}

	for _, srv := range normalServers {
		c := buildXrayConfig(user, srv, profileFull)
		c["remarks"] = serverRemark(srv)
		configs = append(configs, c)
	}

	// Auto-balancer — В КОНЦЕ списка чтобы не менять "default selection"
	// у уже подключившихся клиентов (HAPP по умолчанию выбирает первую
	// запись). Эмитим только когда серверов ≥2.
	if len(servers) >= 2 {
		configs = append(configs, buildAutoXrayConfig(user, servers))
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(configs)
}

type routingProfile int

const (
	profileFull    routingProfile = iota // ⚡ Обычный VPN (раньше "Весь трафик")
	profileBypass                        // 🚀 Обход блокировок
	profileYoutube                       // 🎬 YouTube без рекламы
	// profileServer — маркер "роут по конкретному серверу".
	// Используется только в base64-листе подписки: каждый активный сервер
	// генерирует отдельную VLESS-ссылку с remarks="{flag} {name}" (без
	// profile-префикса). В будущем когда появится несколько VPS (DE/FI/TR)
	// юзер сможет выбирать географию подключения вручную.
	// В JSON-формате НЕ используется — там по-прежнему 3 routing-профиля.
	profileServer
)

// defaultProfiles — те, что генерируются один раз (на "лучший" сервер по load).
// Порядок важен: он же порядок в UI клиента подписки.
var defaultProfiles = []routingProfile{profileFull, profileBypass, profileYoutube}

func (p routingProfile) label() string {
	switch p {
	case profileFull:
		return "⚡ Обычный VPN"
	case profileBypass:
		return "🚀 Обход блокировок"
	case profileYoutube:
		return "🎬 YouTube без рекламы"
	}
	return "unknown"
}

// buildXrayConfig — полный Xray-конфиг для одного сервера с заданным routing-профилем.
// Структура совпадает с референсом vpn_data_json/wisekeys/*.json.
func buildXrayConfig(user *pb.VPNUser, srv *pb.Server, profile routingProfile) map[string]interface{} {
	return map[string]interface{}{
		"remarks":   fmt.Sprintf("%s · %s %s", profile.label(), flagEmoji(srv.GetCountryCode()), srv.GetName()),
		"dns":       buildDNS(profile),
		"inbounds":  buildInbounds(),
		"log":       map[string]interface{}{"loglevel": "warning"},
		"outbounds": buildOutbounds(user, srv),
		"routing":   buildRouting(profile),
	}
}

// buildDNS — DNS-серверы конфига:
//   - bypass/full: Cloudflare DoH (1.1.1.1) для резолва заблокированных.
//   - youtube: AdGuard DoH (dns.adguard-dns.com) чтобы резать рекламу YT на DNS-уровне.
//
// Для RU-зоны во всех профилях добавляем отдельный split: `geosite:category-ru`
// идёт через Google DNS (не через proxy) — иначе RU-ресурсы могут тормозить.
//
// ВНИМАНИЕ: для LTE-серверов (priority>0 + RU-SNI) этот блок переопределяется
// на `buildPlainDNS()` в writeJSONFormat — DoH-эндпоинты (`cloudflare-dns.com:443`)
// могут блокироваться DPI в проблемных регионах (Хакасия), потому что их SNI
// тоже не в whitelist'е оператора.
func buildDNS(profile routingProfile) map[string]interface{} {
	primary := "https://cloudflare-dns.com/dns-query"
	if profile == profileYoutube {
		primary = "https://dns.adguard-dns.com/dns-query"
	}
	return map[string]interface{}{
		"hosts": map[string]string{
			"cloudflare-dns.com":    "1.1.1.1",
			"dns.google":            "8.8.8.8",
			"dns.adguard-dns.com":   "94.140.14.14",
		},
		"queryStrategy": "UseIPv4",
		"servers": []interface{}{
			primary,
			map[string]interface{}{
				"address": "https://dns.google/dns-query",
				"domains": []string{"geosite:category-ru"},
			},
		},
	}
}

// buildPlainDNS — простой UDP DNS на Cloudflare anycast `1.1.1.1`/`1.0.0.1`.
//
// Используется как override для LTE-серверов (priority>0, server_names в РУ-TLD):
// в проблемных регионах (Хакасия с белым DPI-списком) DoH-эндпоинты могут
// дропаться по SNI — `cloudflare-dns.com` не в whitelist'е оператора.
// Plain UDP DNS (port :53) такого SNI не несёт, проходит свободно.
//
// Минусы:
//   - провайдер юзера видит DNS-запросы в открытую (приватность ниже DoH);
//   - подвержен DNS-spoofing'у на уровне ISP (если включают MITM-DNS).
//
// Для LTE-кейса приоритет «работает» > «приватно», поэтому ок.
// Совпадает с подходом коммерческих VPN (см. memory 2026-05-07, анализ конкурента).
func buildPlainDNS() map[string]interface{} {
	return map[string]interface{}{
		"queryStrategy": "UseIP",
		"servers": []interface{}{
			"1.1.1.1",
			"1.0.0.1",
		},
	}
}

// isRussianSNI — server_names сервера в РУ-TLD (`.ru` / `.рф` / `.su` /
// `.xn--p1ai` punycode .рф). Используется для детекции «LTE-style» серверов
// без введения дополнительной колонки в БД: если SNI российский, сервер
// предназначен для регионов с белым DPI-списком, ему нужен plain DNS.
//
// Этого хватает для нашего use-case: каскадный сервер тоже priority>0,
// но его server_names = `apple.com` (то же что у обычных), поэтому он
// корректно НЕ попадёт под override DNS и продолжит использовать DoH.
func isRussianSNI(sni string) bool {
	s := strings.ToLower(strings.TrimSpace(sni))
	return strings.HasSuffix(s, ".ru") ||
		strings.HasSuffix(s, ".рф") ||
		strings.HasSuffix(s, ".su") ||
		strings.HasSuffix(s, ".xn--p1ai")
}

func buildInbounds() []map[string]interface{} {
	// sniffing.routeOnly=false — как в reference vpn_data_json/wisekeys/.
	// Старые Xray ядра (встроенные в Happ 4.8) требуют это поле явно.
	sniffing := map[string]interface{}{
		"destOverride": []string{"http", "tls", "quic"},
		"enabled":      true,
		"routeOnly":    false,
	}
	return []map[string]interface{}{
		{
			"listen":   "127.0.0.1",
			"port":     10808,
			"protocol": "socks",
			"settings": map[string]interface{}{"auth": "noauth", "udp": true},
			"sniffing": sniffing,
			"tag":      "socks",
		},
		{
			"listen":   "127.0.0.1",
			"port":     10809,
			"protocol": "http",
			"settings": map[string]interface{}{"allowTransparent": false},
			"sniffing": sniffing,
			"tag":      "http",
		},
	}
}

// buildOutbounds — proxy (VLESS+Reality) + direct (freedom) + block (blackhole).
// Всегда одинаковый для всех профилей — различия только в routing.
// Минимальный набор полей для совместимости со старыми Xray-ядрами (Happ 4.8
// ломается на лишних `level`/`show`/`spiderX` с ошибкой "Invalid integer range").
func buildOutbounds(user *pb.VPNUser, srv *pb.Server) []map[string]interface{} {
	return []map[string]interface{}{
		{
			"protocol": "vless",
			"settings": map[string]interface{}{
				"vnext": []map[string]interface{}{
					{
						"address": srv.GetHost(),
						"port":    srv.GetPort(),
						"users": []map[string]interface{}{
							{
								"encryption": "none",
								"flow":       user.GetFlow(),
								"id":         user.GetUuid(),
							},
						},
					},
				},
			},
			"streamSettings": map[string]interface{}{
				"network":  "tcp",
				"security": "reality",
				"realitySettings": map[string]interface{}{
					"fingerprint": "chrome",
					"publicKey":   srv.GetPublicKey(),
					"serverName":  srv.GetServerNames(),
					"shortId":     srv.GetShortId(),
				},
			},
			"tag": "proxy",
		},
		{
			"protocol": "freedom",
			"tag":      "direct",
		},
		{
			"protocol": "blackhole",
			"tag":      "block",
		},
	}
}

// buildRouting — правила для каждого из 3 профилей.
// Порядок правил важен: Xray применяет первое совпавшее.
func buildRouting(profile routingProfile) map[string]interface{} {
	rules := []map[string]interface{}{
		// 1. Локальные сети всегда direct — иначе SSH/LAN сломаются.
		{
			"ip":          localIPNets,
			"outboundTag": "direct",
			"type":        "field",
		},
		// 2. Apple/iCloud всегда direct во всех профилях (push и FaceTime).
		{
			"ip":          appleIPNets,
			"outboundTag": "direct",
			"type":        "field",
		},
		// 3. Bittorrent никогда не гоним через VPN — может быть abuse.
		{
			"outboundTag": "direct",
			"protocol":    []string{"bittorrent"},
			"type":        "field",
		},
	}

	switch profile {
	case profileBypass:
		// RU-домены/IP → direct, остальное → proxy (неявно).
		rules = append(rules,
			map[string]interface{}{
				"domain":      append(append([]string{}, ruDirectDomains...), ruDirectRegexp...),
				"outboundTag": "direct",
				"type":        "field",
			},
			map[string]interface{}{
				"ip":          ruDirectIPNets,
				"outboundTag": "direct",
				"type":        "field",
			},
			// Всё остальное → proxy (явно, как в референсе).
			map[string]interface{}{
				"network":     "tcp,udp",
				"outboundTag": "proxy",
				"type":        "field",
			},
		)

	case profileFull:
		// Только локалки/Apple/BT направлены выше. Всё остальное → proxy.
		// Правило `geosite:category-ads-all` убрали (2026-05-07): часть клиентов
		// (Happ Android и др.) идёт со старым/обрезанным geosite.dat → ядро
		// падает с "missing CATEGORY-ADS-ALL section". Блокировка рекламы
		// доступна юзерам через AdGuard DoH (см. profileYoutube DNS-блок).
		rules = append(rules,
			map[string]interface{}{
				"network":     "tcp,udp",
				"outboundTag": "proxy",
				"type":        "field",
			},
		)

	case profileYoutube:
		// YT-домены + RU-блокировки → proxy. Остальное → direct.
		// Анти-реклама — через AdGuard DoH в DNS-секции (см. buildDNS),
		// routing-правило с geosite:category-ads-all убрано (см. profileFull).
		rules = append(rules,
			map[string]interface{}{
				"domain":      youtubeProxyDomains,
				"outboundTag": "proxy",
				"type":        "field",
			},
			// RU-домены/IP → direct.
			map[string]interface{}{
				"domain":      append(append([]string{}, ruDirectDomains...), ruDirectRegexp...),
				"outboundTag": "direct",
				"type":        "field",
			},
			map[string]interface{}{
				"ip":          ruDirectIPNets,
				"outboundTag": "direct",
				"type":        "field",
			},
			// Остальное → direct (экономит прокси-трафик).
			map[string]interface{}{
				"network":     "tcp,udp",
				"outboundTag": "direct",
				"type":        "field",
			},
		)
	}

	return map[string]interface{}{
		"domainStrategy": "IPIfNonMatch",
		"domainMatcher":  "hybrid",
		"rules":          rules,
	}
}

// --- helpers ---

// pickDefaultServer возвращает «дефолтный» сервер для трёх режимов подписки.
// Логика:
//  1. Среди normal-серверов (priority == 0) ищем первый с country_code,
//     совпадающим с defaultCountry → берём его (закреплённая страна).
//  2. Если такой не нашёлся — первый normal-сервер (servers отсортированы по
//     load_percent, наименее нагруженный — первый).
//  3. Если normal-серверов нет совсем — fallback на servers[0] (любой
//     priority-сервер).
//  4. Если servers пустой — nil.
//
// Priority-серверы НЕ становятся дефолтом: они — точечные опции (LTE-обход,
// каскад через РФ), а не «универсальный VPN». Юзер выбирает их явно из
// списка под главным режимом.
func pickDefaultServer(servers []*pb.Server, country string) *pb.Server {
	if len(servers) == 0 {
		return nil
	}
	var firstNormal *pb.Server
	for _, s := range servers {
		if s.GetPriority() != 0 {
			continue
		}
		if firstNormal == nil {
			firstNormal = s
		}
		if country != "" && strings.EqualFold(s.GetCountryCode(), country) {
			return s
		}
	}
	if firstNormal != nil {
		return firstNormal
	}
	return servers[0]
}

// flagEmoji — ISO-код страны ("DE", "RU", "FI") → regional indicator эмоджи.
// Возвращает 🏳 для неизвестных/пустых.
func flagEmoji(countryCode string) string {
	cc := strings.ToUpper(strings.TrimSpace(countryCode))
	if len(cc) != 2 {
		return "🏳"
	}
	const base = 0x1F1E6 // regional indicator A
	var out []rune
	for _, c := range cc {
		if c < 'A' || c > 'Z' {
			return "🏳"
		}
		out = append(out, rune(int(c-'A')+base))
	}
	return string(out)
}

// parseRFC3339 — tolerant парсер времени из proto.
// При ошибке возвращает time.Now() + 24h (клиент не увидит "просрочено" случайно).
func parseRFC3339(s string) time.Time {
	if s == "" {
		return time.Now().Add(24 * time.Hour)
	}
	t, err := time.Parse("2006-01-02T15:04:05Z", s)
	if err != nil {
		return time.Now().Add(24 * time.Hour)
	}
	return t
}

func safePrefix(token string) string {
	if len(token) > 8 {
		return token[:8] + "…"
	}
	return token
}

// tokenPrefix — ASCII-only prefix токена для HTTP-заголовков (filename, etc).
func tokenPrefix(token string, n int) string {
	if len(token) > n {
		return token[:n]
	}
	return token
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func isGRPCNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "notfound") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "code = notfound")
}
