package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	pb "github.com/vpn/shared/pkg/proto/vpn/v1"
)

// sniInPool — test-helper: проверяет что значение есть в множестве пула SNI.
func sniInPool(got string, pool []string) bool {
	for _, p := range pool {
		if got == p {
			return true
		}
	}
	return false
}

// TestPickSNI_Distribution — pickSNI должен возвращать значения только из
// заданного пула. Делаем 100 итераций, проверяем каждое значение и что
// в итоге задействовано ≥2 разных элементов (статистически почти 1).
//
// Это ключевая инвариант многоэлементного пула — без неё вся идея
// «декорреляция SNI per VLESS-link» бессмысленна.
func TestPickSNI_Distribution(t *testing.T) {
	pool := []string{"vk.com", "max.ru", "yandex.ru", "github.com"}
	srv := &pb.Server{ServerNames: pool}

	seen := map[string]int{}
	const iter = 100
	for i := 0; i < iter; i++ {
		got := pickSNI(srv)
		if !sniInPool(got, pool) {
			t.Fatalf("pickSNI returned %q not in pool %v", got, pool)
		}
		seen[got]++
	}
	if len(seen) < 2 {
		t.Errorf("pickSNI not random: only %d distinct values in %d iterations: %v",
			len(seen), iter, seen)
	}
}

// TestPickSNI_SingleElement — для одно-элементного пула pickSNI всегда
// возвращает этот элемент (детерминированно).
func TestPickSNI_SingleElement(t *testing.T) {
	srv := &pb.Server{ServerNames: []string{"apple.com"}}
	for i := 0; i < 5; i++ {
		if got := pickSNI(srv); got != "apple.com" {
			t.Errorf("iter %d: want apple.com, got %q", i, got)
		}
	}
}

// TestPickSNI_EmptyPool — пустой пул → "" (не паника). Caller сам
// решает что делать с этим инвалидным сервером.
func TestPickSNI_EmptyPool(t *testing.T) {
	srv := &pb.Server{ServerNames: nil}
	if got := pickSNI(srv); got != "" {
		t.Errorf("empty pool: want \"\", got %q", got)
	}
	srv = &pb.Server{ServerNames: []string{}}
	if got := pickSNI(srv); got != "" {
		t.Errorf("empty pool slice: want \"\", got %q", got)
	}
}

// TestServerIsRussian_AnyMatches — серверу достаточно одного RU-TLD SNI
// в пуле, чтобы быть классифицированным как «русский» (LTE-style).
// Это даёт plain DNS + пустой shortId.
func TestServerIsRussian_AnyMatches(t *testing.T) {
	cases := []struct {
		name string
		pool []string
		want bool
	}{
		{"all RU", []string{"vk.com", "max.ru", "yandex.ru"}, true},
		{"first RU only", []string{"ads.x5.ru", "github.com"}, true},
		{"last RU only", []string{"apple.com", "почта.рф"}, true},
		{"middle RU only", []string{"github.com", "example.su", "apple.com"}, true},
		{"punycode .рф", []string{"example.xn--p1ai"}, true},
		{"none RU", []string{"github.com", "apple.com", "creative-demo.dh.sg"}, false},
		{"empty", nil, false},
		{"empty slice", []string{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := &pb.Server{ServerNames: c.pool}
			if got := serverIsRussian(srv); got != c.want {
				t.Errorf("pool=%v: want %v, got %v", c.pool, c.want, got)
			}
		})
	}
}

// TestBuildVLESSLink_RandomSNIInPool — за N запусков параметр sni в URI
// принимает значения только из пула, и хотя бы 2 разных значения встречаются.
// Это смоук-тест на интегрированный pickSNI(buildVLESSLink) — без него
// миграция multi-SNI бесполезна (юзер всегда коннектится с одним и тем же).
func TestBuildVLESSLink_RandomSNIInPool(t *testing.T) {
	user := fixtureUser()
	pool := []string{"vk.com", "max.ru", "grishchenkov.ru", "mail.hohlov.tech"}
	srv := &pb.Server{
		Id: 144, Name: "[LTE] RU", CountryCode: "RU",
		Host: "91.184.245.196", Port: 1443,
		PublicKey:   "GTmCq-rBPvmRTuh7tb_0xZGg7duSUFSB85yXkERZBWw",
		ShortId:     "abc123",
		ServerNames: pool,
	}

	seen := map[string]int{}
	const iter = 50
	for i := 0; i < iter; i++ {
		link := buildVLESSLink(user, srv, "test")
		// Извлечь sni= из query
		qStart := strings.Index(link, "sni=")
		if qStart < 0 {
			t.Fatalf("link has no sni= param: %s", link)
		}
		end := strings.IndexAny(link[qStart+4:], "&#")
		if end < 0 {
			end = len(link) - qStart - 4
		}
		got := link[qStart+4 : qStart+4+end]
		if !sniInPool(got, pool) {
			t.Fatalf("iter %d: sni=%q not in pool %v", i, got, pool)
		}
		seen[got]++
	}
	if len(seen) < 2 {
		t.Errorf("VLESS link sni not random: %d distinct in %d iterations: %v",
			len(seen), iter, seen)
	}
}

// TestWriteJSONFormat_PerServerSNIRandom — в JSON-формате (HAPP) каждый
// серверный конфиг должен иметь serverName из пула. Проверяем по 3+
// записям что serverName всегда валидный.
func TestWriteJSONFormat_PerServerSNIRandom(t *testing.T) {
	pool := []string{"vk.com", "max.ru", "grishchenkov.ru"}
	srv := &pb.Server{
		Id: 144, Name: "[LTE] RU", CountryCode: "RU",
		Host: "91.184.245.196", Port: 1443,
		PublicKey:   "GTmCq-rBPvmRTuh7tb_0xZGg7duSUFSB85yXkERZBWw",
		ShortId:     "abc123",
		ServerNames: pool,
		Priority:    10, // priority>0 → сразу под главным режимом
	}
	cfg := &pb.GetSubscriptionConfigResponse{
		VpnUser: fixtureUser(),
		Servers: []*pb.Server{srv},
	}

	rec := httptest.NewRecorder()
	writeJSONFormat(rec, cfg, "")

	var configs []map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &configs); err != nil {
		t.Fatalf("decode: %v", err)
	}

	checked := 0
	for _, c := range configs {
		outs, _ := c["outbounds"].([]interface{})
		for _, o := range outs {
			out := o.(map[string]interface{})
			if out["protocol"] != "vless" {
				continue
			}
			stream, _ := out["streamSettings"].(map[string]interface{})
			reality, _ := stream["realitySettings"].(map[string]interface{})
			sni, _ := reality["serverName"].(string)
			if !sniInPool(sni, pool) {
				t.Errorf("config %q: serverName=%q not in pool %v",
					c["remarks"], sni, pool)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Error("no vless outbounds found to check")
	}
}
