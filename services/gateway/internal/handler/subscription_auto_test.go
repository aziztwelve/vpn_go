package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	pb "github.com/vpn/shared/pkg/proto/vpn/v1"
)

// fixtureServer2 — второй сервер (Финляндия) для multi-server тестов
// auto-balancer'а. Чуть отличается host/port/keys от fixtureServer (DE).
func fixtureServer2() *pb.Server {
	return &pb.Server{
		Id:          2,
		Name:        "Finland",
		Location:    "Helsinki",
		CountryCode: "FI",
		Host:        "95.85.253.217",
		Port:        8443,
		PublicKey:   "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij1234567",
		ShortId:     "deadbeefcafe",
		ServerNames: "github.com",
		IsActive:    true,
	}
}

// TestBuildAutoXrayConfig_Structure — проверяет что auto-конфиг имеет все
// обязательные блоки (включая burstObservatory) и сериализуется в JSON.
func TestBuildAutoXrayConfig_Structure(t *testing.T) {
	user := fixtureUser()
	servers := []*pb.Server{fixtureServer(), fixtureServer2()}

	cfg := buildAutoXrayConfig(user, servers)

	for _, k := range []string{"remarks", "dns", "inbounds", "log", "outbounds", "routing", "burstObservatory"} {
		if _, ok := cfg[k]; !ok {
			t.Errorf("missing key %q in auto config", k)
		}
	}

	remarks, _ := cfg["remarks"].(string)
	if !strings.Contains(remarks, "АВТО ВЫБОР") {
		t.Errorf("auto config remarks must contain 'АВТО ВЫБОР', got %q", remarks)
	}

	// JSON-сериализация должна работать без ошибки — Xray-клиент будет
	// парсить именно JSON, любая неудачная сериализация = сломанный конфиг.
	if _, err := json.Marshal(cfg); err != nil {
		t.Errorf("auto config json marshal failed: %v", err)
	}
}

// TestBuildAutoOutbounds_Tags — проверяет что outbound-теги имеют формат
// "proxy-{idx}" (1-indexed), и что direct/block добавлены в конец.
// Префикс "proxy" критичен: subjectSelector в burstObservatory и selector
// в balancer оба используют prefix-match по этой строке.
func TestBuildAutoOutbounds_Tags(t *testing.T) {
	user := fixtureUser()
	servers := []*pb.Server{fixtureServer(), fixtureServer2()}

	outs := buildAutoOutbounds(user, servers)

	if len(outs) != len(servers)+2 {
		t.Fatalf("want %d outbounds (N proxy + direct + block), got %d", len(servers)+2, len(outs))
	}

	wantTags := []string{"proxy-1", "proxy-2", "direct", "block"}
	for i, want := range wantTags {
		if outs[i]["tag"] != want {
			t.Errorf("outbound[%d].tag: want %q, got %v", i, want, outs[i]["tag"])
		}
	}

	// Все proxy-* должны иметь правильный publicKey/serverName из соответствующего srv.
	for i, srv := range servers {
		stream, _ := outs[i]["streamSettings"].(map[string]interface{})
		reality, _ := stream["realitySettings"].(map[string]interface{})
		if reality["publicKey"] != srv.PublicKey {
			t.Errorf("proxy-%d publicKey: want %q, got %v", i+1, srv.PublicKey, reality["publicKey"])
		}
	}
}

// TestBuildAutoRouting_FallbackBlock — security invariant: при падении
// всех нод трафик должен идти в blackhole, а НЕ в direct. Direct = утечка
// реального IP, что для VPN-сервиса недопустимо.
func TestBuildAutoRouting_FallbackBlock(t *testing.T) {
	routing := buildAutoRouting()
	balancers, _ := routing["balancers"].([]map[string]interface{})
	if len(balancers) != 1 {
		t.Fatalf("want 1 balancer, got %d", len(balancers))
	}
	if got := balancers[0]["fallbackTag"]; got != "block" {
		t.Errorf("balancer fallbackTag must be 'block' (anti-leak), got %v", got)
	}

	// Strategy должна быть leastLoad — иначе RTT-замеры observatory не используются.
	strategy, _ := balancers[0]["strategy"].(map[string]interface{})
	if strategy["type"] != "leastLoad" {
		t.Errorf("balancer strategy must be leastLoad, got %v", strategy["type"])
	}

	// Selector должен быть префиксный "proxy" — иначе balancer не увидит outbound'ы.
	sel, _ := balancers[0]["selector"].([]string)
	if len(sel) != 1 || sel[0] != "proxy" {
		t.Errorf("balancer selector must be [\"proxy\"], got %v", sel)
	}
}

// TestBuildAutoRouting_AppleAndBT — те же критичные инварианты что и для
// существующих профилей: Apple-IP и bittorrent должны идти direct даже
// в auto-режиме.
func TestBuildAutoRouting_AppleAndBT(t *testing.T) {
	routing := buildAutoRouting()
	rules, _ := routing["rules"].([]map[string]interface{})

	foundApple, foundBT := false, false
	for _, rule := range rules {
		ips, _ := rule["ip"].([]string)
		for _, ip := range ips {
			if ip == "17.0.0.0/8" {
				foundApple = true
				if rule["outboundTag"] != "direct" {
					t.Errorf("auto: Apple IP must be direct, got %v", rule["outboundTag"])
				}
			}
		}
		protos, _ := rule["protocol"].([]string)
		for _, proto := range protos {
			if proto == "bittorrent" {
				foundBT = true
				if rule["outboundTag"] != "direct" {
					t.Errorf("auto: bittorrent must be direct, got %v", rule["outboundTag"])
				}
			}
		}
	}
	if !foundApple {
		t.Error("auto: Apple IP rule missing")
	}
	if !foundBT {
		t.Error("auto: bittorrent rule missing")
	}

	// Last rule = balancer → весь не-RU/не-Apple трафик уходит в balancer.
	last := rules[len(rules)-1]
	if last["balancerTag"] != "Auto_Balancer" {
		t.Errorf("auto: last rule must use balancerTag 'Auto_Balancer', got %v", last["balancerTag"])
	}
}

// TestBuildBurstObservatory_Defaults — параметры observatory'а должны
// соответствовать референсу (lidervpn/АВТО ВЫБОР.json), иначе RTT-замеры
// либо слишком частые (нагрузка), либо слишком редкие (плохой failover).
func TestBuildBurstObservatory_Defaults(t *testing.T) {
	obs := buildBurstObservatory()

	sel, _ := obs["subjectSelector"].([]string)
	if len(sel) != 1 || sel[0] != "proxy" {
		t.Errorf("subjectSelector must be [\"proxy\"], got %v", sel)
	}

	ping, _ := obs["pingConfig"].(map[string]interface{})
	if ping["destination"] != "http://www.gstatic.com/generate_204" {
		t.Errorf("ping destination should be gstatic 204 endpoint, got %v", ping["destination"])
	}
	if ping["interval"] != "1m" {
		t.Errorf("ping interval should be 1m, got %v", ping["interval"])
	}
	if ping["timeout"] != "3s" {
		t.Errorf("ping timeout should be 3s, got %v", ping["timeout"])
	}
}

// TestWriteJSONFormat_AddsAutoWhenMultipleServers — на 2+ серверах в JSON-ответе
// должна появиться запись «🌐 АВТО ВЫБОР» В КОНЦЕ списка (после 3×N per-server-
// per-profile конфигов).
func TestWriteJSONFormat_AddsAutoWhenMultipleServers(t *testing.T) {
	cfg := &pb.GetSubscriptionConfigResponse{
		VpnUser: fixtureUser(),
		Servers: []*pb.Server{fixtureServer(), fixtureServer2()},
	}

	rec := httptest.NewRecorder()
	writeJSONFormat(rec, cfg)

	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}

	var got []map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	// 2 servers × 3 profiles + 1 auto = 7 entries.
	want := len(cfg.Servers)*len(defaultProfiles) + 1
	if len(got) != want {
		t.Errorf("want %d configs, got %d", want, len(got))
	}

	// Auto-конфиг — ПОСЛЕДНИЙ.
	last := got[len(got)-1]
	remarks, _ := last["remarks"].(string)
	if !strings.Contains(remarks, "АВТО ВЫБОР") {
		t.Errorf("last config must be auto, got remarks=%q", remarks)
	}
	if _, ok := last["burstObservatory"]; !ok {
		t.Error("last config must have burstObservatory block")
	}
}

// TestWriteJSONFormat_NoAutoForSingleServer — с одним сервером auto-балансер
// бессмыслен (один кандидат). Должно быть ровно 3×1 = 3 конфига.
func TestWriteJSONFormat_NoAutoForSingleServer(t *testing.T) {
	cfg := &pb.GetSubscriptionConfigResponse{
		VpnUser: fixtureUser(),
		Servers: []*pb.Server{fixtureServer()},
	}

	rec := httptest.NewRecorder()
	writeJSONFormat(rec, cfg)

	var got []map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if len(got) != len(defaultProfiles) {
		t.Errorf("want %d configs (no auto for single server), got %d", len(defaultProfiles), len(got))
	}

	// Ни в одной из записей не должно быть burstObservatory.
	for i, c := range got {
		if _, ok := c["burstObservatory"]; ok {
			t.Errorf("config[%d] should not have burstObservatory in single-server case", i)
		}
	}
}

// TestWriteJSONFormat_NoAutoForZeroServers — крайний кейс: подписка без
// активных серверов (cfg.Servers пустой). Должен вернуться [] без падения.
func TestWriteJSONFormat_NoAutoForZeroServers(t *testing.T) {
	cfg := &pb.GetSubscriptionConfigResponse{
		VpnUser: fixtureUser(),
		Servers: nil,
	}

	rec := httptest.NewRecorder()
	writeJSONFormat(rec, cfg)

	var got []map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty array for no servers, got %d configs", len(got))
	}
}
