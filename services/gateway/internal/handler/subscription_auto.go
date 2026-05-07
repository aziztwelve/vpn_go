package handler

import (
	"fmt"

	pb "github.com/vpn/shared/pkg/proto/vpn/v1"
)

// «🌐 АВТО ВЫБОР» — конфиг с автоматическим выбором лучшего сервера через
// burstObservatory (RTT-пингер) + leastLoad balancer.
//
// Идея взята у lidervpn (Remnawave-панели). Все активные серверы становятся
// отдельными outbound'ами с тегами proxy-1..proxy-N. Xray-core каждую минуту
// пингует каждый (HTTP GET на gstatic.com/generate_204) ЧЕРЕЗ САМ outbound и
// замеряет реальный «клиент → VPS → интернет» RTT. На каждом новом TCP-
// соединении balancer выбирает сервер с наименьшим RTT. При падении ноды
// (RTT > 1s или таймаут) — авто-переключение на следующую лучшую за ≤1 минуту.
//
// Routing-стратегия аналогична profileBypass: RU/Apple/локалки → direct,
// остальное → balancer. Это даёт «обход блокировок» × «авто-география» в
// одном клике без знания юзером где какая VPS.
//
// Вызывающий код обязан передать servers длиной ≥ 2 — с одним сервером
// balancer бессмыслен (будет один кандидат, expected: 2 не выполнится).
func buildAutoXrayConfig(user *pb.VPNUser, servers []*pb.Server) map[string]interface{} {
	return map[string]interface{}{
		"remarks":   "🌐 АВТО ВЫБОР",
		"dns":       buildDNS(profileBypass), // тот же DNS-сплит что и для Bypass
		"inbounds":  buildInbounds(),
		"log":       map[string]interface{}{"loglevel": "warning"},
		"outbounds": buildAutoOutbounds(user, servers),
		"routing":   buildAutoRouting(),
		// burstObservatory должен быть ровно top-level — это standalone-feature
		// Xray, не часть routing. См. xray-core/app/observatory/burst.
		"burstObservatory": buildBurstObservatory(),
	}
}

// buildAutoOutbounds — N proxy-* outbound'ов (по одному на сервер) +
// freedom (direct) + blackhole (block).
//
// Tag-format "proxy-{idx}" (1-indexed). subjectSelector в burstObservatory и
// selector в balancer оба используют prefix-match по строке "proxy", поэтому
// все эти outbound'ы автоматически становятся кандидатами для пинг-замеров и
// балансировки.
func buildAutoOutbounds(user *pb.VPNUser, servers []*pb.Server) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(servers)+2)
	for i, srv := range servers {
		out = append(out, map[string]interface{}{
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
					"shortId":     clientShortID(srv),
				},
			},
			"tag": fmt.Sprintf("proxy-%d", i+1),
		})
	}
	out = append(out, map[string]interface{}{
		"protocol": "freedom",
		"tag":      "direct",
	})
	out = append(out, map[string]interface{}{
		"protocol": "blackhole",
		"tag":      "block",
	})
	return out
}

// buildBurstObservatory — health-checker для всех outbound с тегом-prefix
// "proxy". Каждые 60s пингует gstatic.com/generate_204 (HTTP 204, ~50 байт)
// через сам outbound и сохраняет RTT.
//
// Параметры консервативные:
//   - interval: 1m   — оверхед ~50B × N серверов в минуту
//   - timeout : 3s   — выше — нода считается dead для текущего тика
//   - sampling: 1    — последний 1 замер хранится в памяти
//   - connectivity: "" — не проверяем "есть ли вообще интернет" перед пингом
//     (избыточно для нашего случая — клиент сам разберётся при offline)
func buildBurstObservatory() map[string]interface{} {
	return map[string]interface{}{
		"subjectSelector": []string{"proxy"},
		"pingConfig": map[string]interface{}{
			"destination":  "http://www.gstatic.com/generate_204",
			"connectivity": "",
			"interval":     "1m",
			"timeout":      "3s",
			"sampling":     1,
		},
	}
}

// buildAutoRouting — bypass-style правила + balancer для прокси-трафика.
//
// Правила в порядке применения (Xray берёт первое совпавшее):
//  1. localIPNets    → direct (LAN/SSH)
//  2. appleIPNets    → direct (push-уведомления, FaceTime — критично для iOS)
//  3. bittorrent     → direct (анти-abuse: хостеры жалуются)
//  4. ruDirectDomains→ direct (RU-домены не через VPN)
//  5. ruDirectIPNets → direct
//  6. tcp,udp        → balancer (Auto_Balancer выберет лучший proxy-N)
//
// Balancer:
//   - selector: ["proxy"] — все proxy-* outbound'ы кандидаты
//   - strategy: leastLoad  — выбирает наименьший RTT из observatory
//   - maxRTT: 1s           — отбраковываем медленные (>1s)
//   - expected: 2          — стратегия держит 2 топ-кандидата параллельно
//     (распределяет трафик между ними); если живых < 2 — расширяет maxRTT
//     чтобы добрать. У нас сейчас ≥3 локаций, при падении 1 остаются 2.
//   - tolerance: 0.05      — переключаться на новую ноду только если она
//     >5% быстрее текущей (анти-flapping; lidervpn ставит 0.01, но при
//     малом числе нод 5% даёт стабильнее behaviour)
//   - fallbackTag: block   — если все ноды мертвы, БЛОКИРУЕМ трафик, не
//     направляем в direct. Для VPN-сервиса утечка реального IP при
//     fallback'е недопустима.
func buildAutoRouting() map[string]interface{} {
	rules := []map[string]interface{}{
		{
			"ip":          localIPNets,
			"outboundTag": "direct",
			"type":        "field",
		},
		{
			"ip":          appleIPNets,
			"outboundTag": "direct",
			"type":        "field",
		},
		{
			"outboundTag": "direct",
			"protocol":    []string{"bittorrent"},
			"type":        "field",
		},
		{
			"domain":      append(append([]string{}, ruDirectDomains...), ruDirectRegexp...),
			"outboundTag": "direct",
			"type":        "field",
		},
		{
			"ip":          ruDirectIPNets,
			"outboundTag": "direct",
			"type":        "field",
		},
		{
			"network":     "tcp,udp",
			"balancerTag": "Auto_Balancer",
			"type":        "field",
		},
	}
	return map[string]interface{}{
		"domainStrategy": "IPIfNonMatch",
		"domainMatcher":  "hybrid",
		"rules":          rules,
		"balancers": []map[string]interface{}{
			{
				"tag":      "Auto_Balancer",
				"selector": []string{"proxy"},
				"strategy": map[string]interface{}{
					"type": "leastLoad",
					"settings": map[string]interface{}{
						"maxRTT":    "1s",
						"expected":  2,
						"baselines": []string{"1s"},
						"tolerance": 0.05,
					},
				},
				"fallbackTag": "block",
			},
		},
	}
}
