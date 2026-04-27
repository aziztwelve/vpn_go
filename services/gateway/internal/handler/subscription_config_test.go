package handler

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	pb "github.com/vpn/shared/pkg/proto/vpn/v1"
)

// fixtureUser — каноничный VPNUser для тестов. UUID задан в hex-формате,
// чтобы проверить что handler его не мутирует.
func fixtureUser() *pb.VPNUser {
	return &pb.VPNUser{
		Id:                1,
		UserId:            42,
		SubscriptionId:    7,
		Uuid:              "550e8400-e29b-41d4-a716-446655440000",
		Email:             "user42@vpn.local",
		Flow:              "xtls-rprx-vision",
		SubscriptionToken: "abc123def456",
	}
}

// fixtureServer — Германия (Hetzner FSN1) с Reality-параметрами.
func fixtureServer() *pb.Server {
	return &pb.Server{
		Id:          1,
		Name:        "Germany",
		Location:    "Falkenstein",
		CountryCode: "DE",
		Host:        "178.104.217.201",
		Port:        8443,
		PublicKey:   "Npb1GRjWa5dEHU0aTPyxQxN4YSnjNSiniwt1IBNOUn0",
		ShortId:     "e01417022de29ba0",
		ServerNames: "github.com",
		IsActive:    true,
	}
}

func TestBuildVLESSLink(t *testing.T) {
	user := fixtureUser()
	srv := fixtureServer()

	got := buildVLESSLink(user, srv, "🚀 Обход блокировок · 🇩🇪 Germany")

	// Проверяем схему + user + host + port.
	if !strings.HasPrefix(got, "vless://550e8400-e29b-41d4-a716-446655440000@178.104.217.201:8443?") {
		t.Errorf("unexpected prefix: %s", got)
	}

	// Разобрать query, проверить Reality-параметры.
	qStart := strings.Index(got, "?")
	qEnd := strings.Index(got, "#")
	if qStart == -1 || qEnd == -1 {
		t.Fatalf("missing ?query or #fragment in: %s", got)
	}
	params, err := url.ParseQuery(got[qStart+1 : qEnd])
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}

	wantParams := map[string]string{
		"encryption": "none",
		"type":       "tcp",
		"security":   "reality",
		"flow":       "xtls-rprx-vision",
		"pbk":        "Npb1GRjWa5dEHU0aTPyxQxN4YSnjNSiniwt1IBNOUn0",
		"sid":        "e01417022de29ba0",
		"sni":        "github.com",
		"fp":         "chrome",
		"spx":        "/",
	}
	for k, want := range wantParams {
		if got := params.Get(k); got != want {
			t.Errorf("param %s: want %q, got %q", k, want, got)
		}
	}

	// Fragment — URL-encoded remarks. Декодируем обратно.
	fragRaw := got[qEnd+1:]
	frag, err := url.QueryUnescape(fragRaw)
	if err != nil {
		t.Fatalf("unescape fragment: %v", err)
	}
	if frag != "🚀 Обход блокировок · 🇩🇪 Germany" {
		t.Errorf("fragment mismatch: got %q", frag)
	}
}

func TestBuildVLESSLink_EmptyRemarks(t *testing.T) {
	got := buildVLESSLink(fixtureUser(), fixtureServer(), "")
	if !strings.HasSuffix(got, "#") {
		t.Errorf("empty remarks should end with '#': %s", got)
	}
}

func TestProfileLabel(t *testing.T) {
	cases := []struct {
		p    routingProfile
		want string
	}{
		{profileFull, "⚡ Обычный VPN"},
		{profileBypass, "🚀 Обход блокировок"},
		{profileYoutube, "🎬 YouTube без рекламы"},
		{routingProfile(999), "unknown"},
	}
	for _, c := range cases {
		if got := c.p.label(); got != c.want {
			t.Errorf("label(%d): want %q, got %q", c.p, c.want, got)
		}
	}
}

func TestFlagEmoji(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"FI", "🇫🇮"},
		{"RU", "🇷🇺"},
		{"DE", "🇩🇪"},
		{"US", "🇺🇸"},
		{"fi", "🇫🇮"},   // case-insensitive
		{" FI ", "🇫🇮"}, // trimmed
		{"", "🏳"},       // empty → белый флаг
		{"X", "🏳"},      // один символ
		{"XYZ", "🏳"},    // три символа
		{"F1", "🏳"},     // не-буква
		{"fi1", "🏳"},    // смешанное
	}
	for _, c := range cases {
		if got := flagEmoji(c.in); got != c.want {
			t.Errorf("flagEmoji(%q): want %q, got %q", c.in, c.want, got)
		}
	}
}

func TestProfileRemark(t *testing.T) {
	srv := fixtureServer()
	got := profileRemark(profileBypass, srv)
	want := "🚀 Обход блокировок · 🇩🇪 Germany"
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

func TestServerRemark(t *testing.T) {
	srv := fixtureServer()
	got := serverRemark(srv)
	want := "🇩🇪 Germany"
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

// TestBuildXrayConfig_Structure — проверяет структуру и инварианты
// результирующих Xray-конфигов для всех 3 профилей. Не snapshot (чтобы
// не падать на добавлении новых RU-доменов), а проверка ключевых полей.
func TestBuildXrayConfig_Structure(t *testing.T) {
	user := fixtureUser()
	srv := fixtureServer()

	for _, p := range defaultProfiles {
		p := p
		t.Run(p.label(), func(t *testing.T) {
			cfg := buildXrayConfig(user, srv, p)

			// Обязательные топ-уровневые ключи.
			for _, k := range []string{"remarks", "dns", "inbounds", "log", "outbounds", "routing"} {
				if _, ok := cfg[k]; !ok {
					t.Errorf("missing key %q in config", k)
				}
			}

			// Remarks должен содержать label профиля и имя сервера с флагом.
			remarks, _ := cfg["remarks"].(string)
			if !strings.Contains(remarks, p.label()) {
				t.Errorf("remarks missing profile label %q: %s", p.label(), remarks)
			}
			if !strings.Contains(remarks, "Germany") {
				t.Errorf("remarks missing server name: %s", remarks)
			}
			if !strings.Contains(remarks, "🇩🇪") {
				t.Errorf("remarks missing flag: %s", remarks)
			}

			// Outbounds — всегда 3 (proxy/direct/block).
			outs, _ := cfg["outbounds"].([]map[string]interface{})
			if len(outs) != 3 {
				t.Fatalf("want 3 outbounds, got %d", len(outs))
			}
			wantTags := []string{"proxy", "direct", "block"}
			for i, w := range wantTags {
				if outs[i]["tag"] != w {
					t.Errorf("outbound[%d].tag: want %q, got %v", i, w, outs[i]["tag"])
				}
			}

			// Проверяем что proxy outbound содержит Reality с правильным publicKey.
			proxy := outs[0]
			stream, _ := proxy["streamSettings"].(map[string]interface{})
			reality, _ := stream["realitySettings"].(map[string]interface{})
			if reality["publicKey"] != srv.PublicKey {
				t.Errorf("reality.publicKey mismatch: %v", reality["publicKey"])
			}
			if reality["serverName"] != srv.ServerNames {
				t.Errorf("reality.serverName mismatch: %v", reality["serverName"])
			}

			// JSON-сериализация должна проходить без ошибки (реальный клиент
			// будет парсить именно JSON).
			if _, err := json.Marshal(cfg); err != nil {
				t.Errorf("json marshal failed: %v", err)
			}
		})
	}
}

// TestBuildRouting_Profiles — проверяет что каждый профиль генерит ожидаемый
// набор "финальных" правил (последнее правило определяет default-behavior).
func TestBuildRouting_Profiles(t *testing.T) {
	cases := []struct {
		profile              routingProfile
		wantFallbackOutbound string
		// Должны присутствовать первые 3 common-правила: localIP, appleIP, bittorrent.
		wantLocalDirect bool
	}{
		{profileFull, "proxy", true},
		{profileBypass, "proxy", true},
		{profileYoutube, "direct", true},
	}

	for _, c := range cases {
		c := c
		t.Run(c.profile.label(), func(t *testing.T) {
			got := buildRouting(c.profile)
			rules, _ := got["rules"].([]map[string]interface{})
			if len(rules) < 4 {
				t.Fatalf("expected at least 4 rules, got %d", len(rules))
			}

			// Проверяем что первое правило — локальные IP → direct.
			if c.wantLocalDirect {
				first := rules[0]
				if first["outboundTag"] != "direct" {
					t.Errorf("first rule must be direct, got %v", first["outboundTag"])
				}
				ips, _ := first["ip"].([]string)
				if len(ips) == 0 {
					t.Errorf("first rule should have ip list")
				}
			}

			// Последнее правило — network "tcp,udp" с fallback outbound.
			last := rules[len(rules)-1]
			if last["network"] != "tcp,udp" {
				t.Errorf("last rule should be network tcp,udp, got %v", last["network"])
			}
			if last["outboundTag"] != c.wantFallbackOutbound {
				t.Errorf("last rule outbound: want %q, got %v", c.wantFallbackOutbound, last["outboundTag"])
			}

			// domainStrategy/domainMatcher — общие для всех.
			if got["domainStrategy"] != "IPIfNonMatch" {
				t.Errorf("domainStrategy mismatch: %v", got["domainStrategy"])
			}
			if got["domainMatcher"] != "hybrid" {
				t.Errorf("domainMatcher mismatch: %v", got["domainMatcher"])
			}
		})
	}
}

// TestBuildDNS_YouTubeUsesAdGuard — критичный инвариант: YouTube-профиль
// должен юзать AdGuard DoH как primary для блокировки YT-рекламы на DNS-уровне.
func TestBuildDNS_YouTubeUsesAdGuard(t *testing.T) {
	dns := buildDNS(profileYoutube)
	servers, _ := dns["servers"].([]interface{})
	if len(servers) == 0 {
		t.Fatal("no DNS servers")
	}
	primary, _ := servers[0].(string)
	if !strings.Contains(primary, "adguard") {
		t.Errorf("YouTube profile must use AdGuard DNS, got %q", primary)
	}

	// Для остальных профилей — cloudflare.
	for _, p := range []routingProfile{profileFull, profileBypass} {
		d := buildDNS(p)
		srvs, _ := d["servers"].([]interface{})
		primary, _ := srvs[0].(string)
		if !strings.Contains(primary, "cloudflare") {
			t.Errorf("profile %s should use Cloudflare DNS, got %q", p.label(), primary)
		}
	}
}

// TestBuildRouting_AlwaysDirectApple — убеждаемся что во ВСЕХ профилях
// Apple IP range (17.0.0.0/8) идёт direct. Иначе ломаются push-уведомления
// на iOS, что катастрофично для UX.
func TestBuildRouting_AlwaysDirectApple(t *testing.T) {
	for _, p := range defaultProfiles {
		routing := buildRouting(p)
		rules, _ := routing["rules"].([]map[string]interface{})

		foundApple := false
		for _, rule := range rules {
			ips, _ := rule["ip"].([]string)
			for _, ip := range ips {
				if ip == "17.0.0.0/8" {
					foundApple = true
					if rule["outboundTag"] != "direct" {
						t.Errorf("profile %s: Apple IP must be direct, got %v", p.label(), rule["outboundTag"])
					}
				}
			}
		}
		if !foundApple {
			t.Errorf("profile %s: Apple IP 17.0.0.0/8 not found in rules", p.label())
		}
	}
}

// TestBuildRouting_AlwaysDirectBitTorrent — трафик BT никогда не должен идти
// через proxy (риск abuse-жалоб от хостеров).
func TestBuildRouting_AlwaysDirectBitTorrent(t *testing.T) {
	for _, p := range defaultProfiles {
		routing := buildRouting(p)
		rules, _ := routing["rules"].([]map[string]interface{})

		foundBT := false
		for _, rule := range rules {
			protos, _ := rule["protocol"].([]string)
			for _, proto := range protos {
				if proto == "bittorrent" {
					foundBT = true
					if rule["outboundTag"] != "direct" {
						t.Errorf("profile %s: bittorrent must be direct, got %v", p.label(), rule["outboundTag"])
					}
				}
			}
		}
		if !foundBT {
			t.Errorf("profile %s: bittorrent rule not found", p.label())
		}
	}
}

func TestTokenPrefix(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"abcdef1234", 4, "abcd"},
		{"abc", 8, "abc"},
		{"", 8, ""},
		{"abcdefgh", 8, "abcdefgh"},
	}
	for _, c := range cases {
		if got := tokenPrefix(c.in, c.n); got != c.want {
			t.Errorf("tokenPrefix(%q,%d): want %q, got %q", c.in, c.n, c.want, got)
		}
	}
}

func TestSafePrefix(t *testing.T) {
	if got := safePrefix("abcdef1234"); got != "abcdef12…" {
		t.Errorf("safePrefix 10 chars: %q", got)
	}
	if got := safePrefix("abc"); got != "abc" {
		t.Errorf("safePrefix short: %q", got)
	}
}

func TestIsGRPCNotFound(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{&dummyErr{"code = NotFound desc = subscription"}, true},
		{&dummyErr{"not found"}, true},
		{&dummyErr{"internal error"}, false},
	}
	for _, c := range cases {
		if got := isGRPCNotFound(c.err); got != c.want {
			t.Errorf("isGRPCNotFound(%v): want %v, got %v", c.err, c.want, got)
		}
	}
}

type dummyErr struct{ msg string }

func (e *dummyErr) Error() string { return e.msg }
