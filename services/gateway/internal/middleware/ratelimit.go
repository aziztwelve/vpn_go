// Package middleware — gateway-специфичные HTTP middleware.
//
// RateLimit — простой in-memory sliding-window лимитер по IP.
// Нужен для публичных эндпоинтов (например /api/v1/subscription/{token}),
// где нет JWT и легко перебирать токены/наваливать запросами.
//
// Реализован без внешних зависимостей (proxy.golang.org не всегда доступен
// из dev-окружения). Один процесс = одна карта, shared-state через RWMutex.
// Для multi-instance gateway (см. таск 08) нужно будет переезжать на Redis.
package middleware

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// bucket — состояние одного IP: окно и счётчик в нём.
// windowStart сбрасывается когда "сейчас" уходит за рамки окна.
type bucket struct {
	count       int
	windowStart time.Time
}

// RateLimiter — потокобезопасный per-IP лимитер.
// Создавать через NewRateLimiter, применять через .Handler (chi-совместимая middleware).
type RateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	limit    int           // сколько запросов разрешено за окно
	window   time.Duration // длина окна (например 1 минута)
	cleanupT *time.Ticker
}

// NewRateLimiter — напр. NewRateLimiter(10, time.Minute) = 10 rpm/IP.
// Запускает фоновую очистку stale-записей раз в window*2.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		buckets:  make(map[string]*bucket),
		limit:    limit,
		window:   window,
		cleanupT: time.NewTicker(window * 2),
	}
	go rl.cleanup()
	return rl
}

// Handler оборачивает next и отдаёт 429 когда лимит превышен.
// IP берётся из X-Real-IP / X-Forwarded-For (chi middleware.RealIP уже
// должен быть подключён раньше — иначе получим адрес ближайшего прокси).
func (rl *RateLimiter) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		allowed, retryAfter := rl.allow(ip)
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":   "rate_limit_exceeded",
				"message": "too many requests, try again later",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allow — потокобезопасный decrement. Возвращает (разрешён, сколько ждать).
// retryAfter — сколько осталось до конца текущего окна, если лимит достигнут.
func (rl *RateLimiter) allow(ip string) (bool, time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[ip]
	if !ok || now.Sub(b.windowStart) >= rl.window {
		rl.buckets[ip] = &bucket{count: 1, windowStart: now}
		return true, 0
	}

	if b.count >= rl.limit {
		return false, rl.window - now.Sub(b.windowStart)
	}
	b.count++
	return true, 0
}

// cleanup раз в window*2 проходит по карте и выкидывает stale-IP,
// чтобы память не росла линейно с количеством уникальных клиентов.
func (rl *RateLimiter) cleanup() {
	for range rl.cleanupT.C {
		cutoff := time.Now().Add(-rl.window * 2)
		rl.mu.Lock()
		for ip, b := range rl.buckets {
			if b.windowStart.Before(cutoff) {
				delete(rl.buckets, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// Stop останавливает фоновый cleanup-тикер. Вызывается в graceful shutdown
// (через closer в app.go). Блокировку карты не нужно — горутина сама
// завершится на закрытом канале ticker'а.
func (rl *RateLimiter) Stop() {
	rl.cleanupT.Stop()
}

// clientIP — извлекаем реальный IP клиента. Ожидаем что middleware.RealIP
// (chi) уже нормализовал r.RemoteAddr из X-Real-IP / X-Forwarded-For.
// Для прямых соединений — парсим host:port.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
