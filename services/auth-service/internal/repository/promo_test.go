// Юнит-тесты на helpers PromoRepository. Реальный Postgres-flow
// (Issue/Redeem/AttachPayment/MarkUsed) покрывается интеграционным
// smoke-тестом на staging.
package repository

import (
	"testing"
	"time"
)

// TestPromoCode_IsExpired — без expires_at == не expired; в будущем →
// не expired; в прошлом → expired.
func TestPromoCode_IsExpired(t *testing.T) {
	now := time.Now()

	t.Run("no expiry", func(t *testing.T) {
		c := &PromoCode{}
		if c.IsExpired() {
			t.Error("nil ExpiresAt should not be expired")
		}
	})

	t.Run("future", func(t *testing.T) {
		future := now.Add(1 * time.Hour)
		c := &PromoCode{ExpiresAt: &future}
		if c.IsExpired() {
			t.Error("future ExpiresAt should not be expired")
		}
	})

	t.Run("past", func(t *testing.T) {
		past := now.Add(-1 * time.Hour)
		c := &PromoCode{ExpiresAt: &past}
		if !c.IsExpired() {
			t.Error("past ExpiresAt should be expired")
		}
	})
}

// TestPromoCode_IsUsed — UsedAt nil/non-nil semantics.
func TestPromoCode_IsUsed(t *testing.T) {
	c := &PromoCode{}
	if c.IsUsed() {
		t.Error("nil UsedAt should not be used")
	}
	now := time.Now()
	c.UsedAt = &now
	if !c.IsUsed() {
		t.Error("non-nil UsedAt should be used")
	}
}

// TestGenerateToken_Format — 32 байта → 64 hex-символа, уникальные.
func TestGenerateToken_Format(t *testing.T) {
	t1, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	if len(t1) != 64 {
		t.Errorf("token len = %d, want 64", len(t1))
	}
	for _, ch := range t1 {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			t.Errorf("token has non-hex char %q in %q", ch, t1)
		}
	}

	// Уникальность — два соседних вызова должны отличаться (вероятность
	// коллизии 1/2^256, не ловим её здесь).
	t2, _ := generateToken()
	if t1 == t2 {
		t.Errorf("two consecutive tokens are equal: %s", t1)
	}
}

// TestIsUniqueViolation — substring-матч 23505 и "duplicate key value".
// Покрытие: оба варианта работают, чужие ошибки не считаются конфликтами.
func TestIsUniqueViolation(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"23505 code", &fakeErr{"ERROR: duplicate key value violates unique constraint \"users_pkey\" (SQLSTATE 23505)"}, true},
		{"only msg", &fakeErr{"duplicate key value"}, true},
		{"unrelated", &fakeErr{"connection refused"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isUniqueViolation(c.err); got != c.want {
				t.Errorf("isUniqueViolation(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

type fakeErr struct{ s string }

func (e *fakeErr) Error() string { return e.s }

// TestDerefInt64 — nil → 0; non-nil → значение.
func TestDerefInt64(t *testing.T) {
	if got := derefInt64(nil); got != 0 {
		t.Errorf("derefInt64(nil) = %d, want 0", got)
	}
	v := int64(42)
	if got := derefInt64(&v); got != 42 {
		t.Errorf("derefInt64(&42) = %d, want 42", got)
	}
}
