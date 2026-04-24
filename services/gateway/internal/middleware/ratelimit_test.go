package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// okHandler — просто 200, чтобы проверить что лимитер его пропускает.
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// mkReq — запрос с заданным IP (имитация прошедшего middleware.RealIP).
func mkReq(ip string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = ip + ":12345"
	return r
}

func TestRateLimiter_AllowsWithinLimit(t *testing.T) {
	rl := NewRateLimiter(3, time.Minute)
	defer rl.Stop()
	h := rl.Handler(okHandler())

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, mkReq("1.2.3.4"))
		if rec.Code != http.StatusOK {
			t.Errorf("request %d: want 200, got %d", i+1, rec.Code)
		}
	}
}

func TestRateLimiter_Blocks4thRequest(t *testing.T) {
	rl := NewRateLimiter(3, time.Minute)
	defer rl.Stop()
	h := rl.Handler(okHandler())

	// Три успешных.
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, mkReq("1.2.3.4"))
	}

	// Четвёртый должен получить 429.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, mkReq("1.2.3.4"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", rec.Code)
	}

	// Retry-After должен быть установлен (целое число секунд > 0).
	if ra := rec.Header().Get("Retry-After"); ra == "" || ra == "0" {
		t.Errorf("Retry-After missing or zero: %q", ra)
	}

	// Content-Type должен быть JSON и body с error-кодом.
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: %q", ct)
	}
	if body := rec.Body.String(); body == "" {
		t.Errorf("empty body")
	}
}

func TestRateLimiter_SeparatePerIP(t *testing.T) {
	rl := NewRateLimiter(2, time.Minute)
	defer rl.Stop()
	h := rl.Handler(okHandler())

	// IP1 исчерпал лимит.
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, mkReq("1.1.1.1"))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, mkReq("1.1.1.1"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("IP1 should be blocked: %d", rec.Code)
	}

	// IP2 должен всё ещё пройти.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, mkReq("2.2.2.2"))
	if rec.Code != http.StatusOK {
		t.Errorf("IP2 should pass: %d", rec.Code)
	}
}

func TestRateLimiter_WindowResets(t *testing.T) {
	// Очень короткое окно чтобы тест быстро шёл.
	rl := NewRateLimiter(1, 50*time.Millisecond)
	defer rl.Stop()
	h := rl.Handler(okHandler())

	// Первый ОК.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, mkReq("3.3.3.3"))
	if rec.Code != http.StatusOK {
		t.Fatalf("first: %d", rec.Code)
	}

	// Сразу второй — 429.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, mkReq("3.3.3.3"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second (immediate): %d", rec.Code)
	}

	// Ждём истечения окна.
	time.Sleep(60 * time.Millisecond)

	// Теперь снова ОК.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, mkReq("3.3.3.3"))
	if rec.Code != http.StatusOK {
		t.Errorf("after window reset: %d", rec.Code)
	}
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		remoteAddr, want string
	}{
		{"1.2.3.4:5678", "1.2.3.4"},
		{"[::1]:8080", "::1"},
		{"nohost", "nohost"}, // fallback: если не парсится, возвращаем как есть
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remoteAddr
		if got := clientIP(r); got != c.want {
			t.Errorf("clientIP(%q): want %q, got %q", c.remoteAddr, c.want, got)
		}
	}
}
