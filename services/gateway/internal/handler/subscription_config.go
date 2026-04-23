package handler

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/vpn/gateway/internal/client"
	"go.uber.org/zap"
)

type SubscriptionConfigHandler struct {
	vpnClient *client.VPNClient
	logger    *zap.Logger
}

func NewSubscriptionConfigHandler(vpnClient *client.VPNClient, logger *zap.Logger) *SubscriptionConfigHandler {
	return &SubscriptionConfigHandler{
		vpnClient: vpnClient,
		logger:    logger,
	}
}

// SubscriptionConfig генерирует подписку для VPN клиентов
// По умолчанию: base64 список VLESS ссылок (для Happ, V2RayNG)
// С параметром ?format=json: полная JSON конфигурация (для продвинутых клиентов)
func (h *SubscriptionConfigHandler) SubscriptionConfig(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if token == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "token required"})
		return
	}

	// TODO: Валидация токена и получение user_id
	// Пока возвращаем конфигурацию для всех

	// Проверяем формат ответа
	format := r.URL.Query().Get("format")
	if format == "json" {
		// Возвращаем полную JSON конфигурацию
		config := h.generateJSONConfig()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Profile-Update-Interval", "1")
		w.Header().Set("Subscription-Userinfo", "upload=0; download=0; total=10737418240; expire=0")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(config)
		return
	}

	// По умолчанию: base64 список VLESS ссылок
	vlessLinks := h.generateVLESSLinks()

	// Объединяем в одну строку (по одной ссылке на строку)
	subscription := ""
	for _, link := range vlessLinks {
		subscription += link + "\n"
	}

	// Кодируем в base64
	encoded := base64.StdEncoding.EncodeToString([]byte(subscription))

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Profile-Update-Interval", "1")
	w.Header().Set("Subscription-Userinfo", "upload=0; download=0; total=10737418240; expire=0")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(encoded))
}

func (h *SubscriptionConfigHandler) generateVLESSLinks() []string {
	// TODO: Получить реальные данные сервера из базы
	// Пока используем хардкод для Finland сервера

	host := "204.168.248.33"
	port := "8443"
	uuid := "550e8400-e29b-41d4-a716-446655440000" // TODO: UUID пользователя
	publicKey := "Npb1GRjWa5dEHU0aTPyxQxN4YSnjNSiniwt1IBNOUn0"
	shortId := "e01417022de29ba0"
	sni := "github.com"

	// Базовые параметры для всех ссылок (порядок важен!)
	params := url.Values{}
	params.Set("security", "reality")
	params.Set("type", "tcp")
	params.Set("headerType", "")
	params.Set("path", "")
	params.Set("host", "")
	params.Set("flow", "xtls-rprx-vision")
	params.Set("sni", sni)
	params.Set("fp", "chrome")
	params.Set("pbk", publicKey)
	params.Set("sid", shortId)

	// 3 ссылки с разными названиями
	links := []string{
		fmt.Sprintf("vless://%s@%s:%s?%s#🚀 Обход блокировок", uuid, host, port, params.Encode()),
		fmt.Sprintf("vless://%s@%s:%s?%s#🔒 Весь трафик", uuid, host, port, params.Encode()),
		fmt.Sprintf("vless://%s@%s:%s?%s#🎬 YouTube без рекламы", uuid, host, port, params.Encode()),
	}

	return links
}

func (h *SubscriptionConfigHandler) generateJSONConfig() map[string]interface{} {
	// Полная JSON конфигурация для продвинутых клиентов
	return map[string]interface{}{
		"dns": map[string]interface{}{
			"hosts": map[string]string{
				"cloudflare-dns.com": "1.1.1.1",
				"dns.google":         "8.8.8.8",
			},
			"queryStrategy": "UseIPv4",
			"servers": []interface{}{
				"https://cloudflare-dns.com/dns-query",
				map[string]interface{}{
					"address": "https://cloudflare-dns.com/dns-query",
					"domains": []string{},
				},
			},
		},
		"inbounds": []map[string]interface{}{
			{
				"listen":   "127.0.0.1",
				"port":     10808,
				"protocol": "socks",
				"settings": map[string]interface{}{
					"auth": "noauth",
					"udp":  true,
				},
				"sniffing": map[string]interface{}{
					"destOverride": []string{"http", "tls"},
					"enabled":      true,
				},
				"tag": "socks-in",
			},
			{
				"listen":   "127.0.0.1",
				"port":     10809,
				"protocol": "http",
				"settings": map[string]interface{}{},
				"sniffing": map[string]interface{}{
					"destOverride": []string{"http", "tls"},
					"enabled":      true,
				},
				"tag": "http-in",
			},
		},
		"log": map[string]interface{}{
			"loglevel": "warning",
		},
		"outbounds": h.generateJSONOutbounds(),
		"routing":   h.generateJSONRouting(),
	}
}

func (h *SubscriptionConfigHandler) generateJSONOutbounds() []map[string]interface{} {
	// TODO: Получить реальные данные сервера из базы
	return []map[string]interface{}{
		{
			"protocol": "vless",
			"settings": map[string]interface{}{
				"vnext": []map[string]interface{}{
					{
						"address": "204.168.248.33",
						"port":    8443,
						"users": []map[string]interface{}{
							{
								"encryption": "none",
								"flow":       "xtls-rprx-vision",
								"id":         "550e8400-e29b-41d4-a716-446655440000",
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
					"publicKey":   "Npb1GRjWa5dEHU0aTPyxQxN4YSnjNSiniwt1IBNOUn0",
					"serverName":  "github.com",
					"shortId":     "e01417022de29ba0",
					"show":        false,
					"spiderX":     "",
				},
				"tcpSettings": map[string]interface{}{},
			},
			"tag": "proxy",
		},
		{
			"protocol": "freedom",
			"settings": map[string]interface{}{},
			"tag":      "direct",
		},
		{
			"protocol": "blackhole",
			"settings": map[string]interface{}{
				"response": map[string]interface{}{
					"type": "http",
				},
			},
			"tag": "block",
		},
	}
}

func (h *SubscriptionConfigHandler) generateJSONRouting() map[string]interface{} {
	return map[string]interface{}{
		"domainStrategy": "IPIfNonMatch",
		"rules": []map[string]interface{}{
			{
				"ip": []string{
					"geoip:private",
				},
				"outboundTag": "direct",
				"type":        "field",
			},
			{
				"domain": []string{
					"geosite:category-ads-all",
				},
				"outboundTag": "block",
				"type":        "field",
			},
			{
				"domain": []string{
					"domain:facebook.com",
					"domain:instagram.com",
					"domain:twitter.com",
					"domain:x.com",
					"domain:youtube.com",
					"domain:googlevideo.com",
					"domain:ytimg.com",
					"domain:telegram.org",
					"domain:t.me",
					"domain:discord.com",
					"domain:discordapp.com",
					"domain:linkedin.com",
					"domain:medium.com",
					"domain:reddit.com",
					"geosite:google",
					"geosite:github",
				},
				"outboundTag": "proxy",
				"type":        "field",
			},
			{
				"network":     "tcp,udp",
				"outboundTag": "direct",
				"type":        "field",
			},
		},
	}
}
