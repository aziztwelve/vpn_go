package handler

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
}

func NewSubscriptionConfigHandler(vpnClient *client.VPNClient, logger *zap.Logger) *SubscriptionConfigHandler {
	return &SubscriptionConfigHandler{vpnClient: vpnClient, logger: logger}
}

// SubscriptionConfig — точка входа HTTP.
func (h *SubscriptionConfigHandler) SubscriptionConfig(w http.ResponseWriter, r *http.Request) {
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

	// HAPP/v2rayN/Hiddify distinguish legitimate VPN subscriptions from generic
	// text by a set of well-known headers. Без них клиент пытается распарсить
	// тело как unknown формат и может упасть на Xray-core с ошибкой типа
	// "Invalid integer range". Референс — extravpn.info, marzban и т.п.
	expireUnix := parseRFC3339(cfg.GetExpiresAt()).Unix()
	userInfo := fmt.Sprintf("upload=0; download=0; total=0; expire=%d", expireUnix)

	// profile-title закодирован в base64 по HAPP-конвенции.
	profileTitle := "base64:" + base64.StdEncoding.EncodeToString([]byte("OsmonAI VPN"))
	// filename используется клиентом как id подписки локально (нужен для
	// корректного обновления — чтобы клиент не создавал дубликат).
	// ASCII-only, без …/emoji — старые клиенты парсят HTTP-header строго.
	filename := "sub-" + tokenPrefix(token, 8)

	w.Header().Set("Profile-Update-Interval", "1")
	w.Header().Set("Subscription-Userinfo", userInfo)
	w.Header().Set("Profile-Title", profileTitle)
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	w.Header().Set("Support-URL", "https://t.me/maydavpnbot")
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

	if r.URL.Query().Get("format") == "json" {
		writeJSONFormat(w, cfg)
		return
	}
	writeBase64Format(w, cfg)
}

// writeBase64Format — стандартный subscription-формат:
// plain-text, base64(VLESS-ссылки по одной на строку).
//
// Структура списка в UI клиента:
//   1) ⚡ Обычный VPN            ← на "лучший" сервер (min load_percent)
//   2) 🚀 Обход блокировок       ← на тот же сервер
//   3) 🎬 YouTube без рекламы    ← на тот же сервер
//   4) 🇫🇮 Finland                ← прямая ссылка на сервер #1
//   5) 🇩🇪 Germany                ← прямая ссылка на сервер #2 (если есть)
//   ...
//
// Первые три — "режимы" (одинаковый outbound, разные remarks для UX-подсказки).
// Остальные — выбор конкретной страны/сервера (на случай нескольких VPS).
// Сейчас `cfg.Servers` отсортирован репо-шкой по load_percent, так что
// servers[0] это наименее загруженный → дефолт для первых 3 "режимов".
func writeBase64Format(w http.ResponseWriter, cfg *pb.GetSubscriptionConfigResponse) {
	servers := cfg.GetServers()
	user := cfg.GetVpnUser()

	var sb strings.Builder

	// Режимы — только на дефолтный (первый, наименее нагруженный) сервер.
	if len(servers) > 0 {
		best := servers[0]
		for _, p := range defaultProfiles {
			sb.WriteString(buildVLESSLink(user, best, profileRemark(p, best)))
			sb.WriteByte('\n')
		}
	}

	// Ссылки на конкретные серверы — имена "{flag} {server.name}", без
	// profile-префикса. Когда будет несколько VPS, юзер выбирает географию.
	for _, srv := range servers {
		sb.WriteString(buildVLESSLink(user, srv, serverRemark(srv)))
		sb.WriteByte('\n')
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(sb.String()))))
}

// buildVLESSLink — VLESS URI с уже готовым #remarks.
// Минимальный набор Reality-параметров совместимый с Xray-core и Happ/v2rayN/Hiddify:
//
//	encryption, type, security, flow, pbk, sid, sni, fp, spx.
//
// remarks передаётся целиком — вызывающий сам решает формат
// (например "⚡ Обычный VPN · 🇫🇮 Finland" или "🇫🇮 Finland").
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

// profileRemark — формат remarks для режима: "⚡ Обычный VPN · 🇫🇮 Finland".
func profileRemark(p routingProfile, srv *pb.Server) string {
	return fmt.Sprintf("%s · %s %s", p.label(), flagEmoji(srv.GetCountryCode()), srv.GetName())
}

// serverRemark — формат remarks для выбора конкретного сервера: "🇫🇮 Finland".
func serverRemark(srv *pb.Server) string {
	return fmt.Sprintf("%s %s", flagEmoji(srv.GetCountryCode()), srv.GetName())
}

// writeJSONFormat — массив полных Xray-конфигов, по одному на (сервер × профиль).
// Так HAPP/v2rayN видят 3 × N "серверов" в списке со своими routing-стратегиями.
//
// 3 профиля:
//  1. 🚀 Обход блокировок — split-tunnel: RU/Apple/локалки → direct, остальное → proxy.
//  2. 🔒 Весь трафик — только локалки → direct, всё остальное → proxy.
//  3. 🎬 YouTube без рекламы — YT через proxy + AdGuard DNS, RU/локалки → direct.
//
// Формат массива JSON-конфигов — HAPP-specific расширение subscription:
// клиент сам разбирает, добавляет каждый объект как сервер.
func writeJSONFormat(w http.ResponseWriter, cfg *pb.GetSubscriptionConfigResponse) {
	configs := make([]map[string]interface{}, 0, len(defaultProfiles)*len(cfg.GetServers()))
	for _, srv := range cfg.GetServers() {
		for _, p := range defaultProfiles {
			configs = append(configs, buildXrayConfig(cfg.GetVpnUser(), srv, p))
		}
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
		rules = append(rules,
			// Реклама → block даже в full VPN (меньше трафика).
			map[string]interface{}{
				"domain":      []string{"geosite:category-ads-all"},
				"outboundTag": "block",
				"type":        "field",
			},
			map[string]interface{}{
				"network":     "tcp,udp",
				"outboundTag": "proxy",
				"type":        "field",
			},
		)

	case profileYoutube:
		// Реклама → block (AdGuard DNS + domain list).
		// YT-домены + RU-блокировки → proxy. Остальное → direct.
		rules = append(rules,
			map[string]interface{}{
				"domain":      []string{"geosite:category-ads-all"},
				"outboundTag": "block",
				"type":        "field",
			},
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
