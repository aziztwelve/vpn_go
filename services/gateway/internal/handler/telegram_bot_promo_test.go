// Тесты на форматтеры сообщений админу для /promo / /promo_status.
// Реальные RPC к auth-service здесь не дёргаем — тесты на уровне рендеринга
// (как и telegram_bot_admin_test.go).
package handler

import (
	"errors"
	"strings"
	"testing"
	"time"

	promopb "github.com/vpn/shared/pkg/proto/promo/v1"
)

// TestPromoTargetDisplay — username даёт @-форму; пустой username
// возвращает tg:<id>; nil → "—".
func TestPromoTargetDisplay(t *testing.T) {
	cases := []struct {
		name string
		in   *promopb.LookupUserResponse
		want string
	}{
		{"with username", &promopb.LookupUserResponse{Username: "aziztwelve", TelegramId: 123}, "@aziztwelve"},
		{"only telegram_id", &promopb.LookupUserResponse{TelegramId: 456}, "tg:456"},
		{"nil target", nil, "—"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := promoTargetDisplay(c.in)
			if got != c.want {
				t.Errorf("promoTargetDisplay = %q, want %q", got, c.want)
			}
		})
	}
}

// TestFormatPromoIssueAdmin_Success — в успехе должен быть «Промо отправлен @user»,
// токен (prefix), URL, /promo_status подсказка для повторной проверки.
func TestFormatPromoIssueAdmin_Success(t *testing.T) {
	h := &TelegramBotHandler{}
	target := &promopb.LookupUserResponse{
		UserId: 100, TelegramId: 1234567, Username: "aziztwelve",
	}
	issue := &promopb.IssuePromoResponse{
		Token:          "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		PromoId:        42,
		ExpiresAtUnix:  time.Now().Add(30 * 24 * time.Hour).Unix(),
		AlreadyExisted: false,
	}
	out := h.formatPromoIssueAdmin(target, issue, "https://cdn.osmonai.com/promo/p/abc...", nil)

	for _, want := range []string{
		"✅ Промо отправлен @aziztwelve",
		"Промо: #42",
		"Token: abcdef01…", // safePrefix первых 8 символов
		"URL: https://cdn.osmonai.com/promo/p/abc...",
		"/promo_status @aziztwelve",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n--- got ---\n%s", want, out)
		}
	}
	// AlreadyExisted=false → не должно быть про "уже был активный"
	if strings.Contains(out, "уже был активный") {
		t.Errorf("unexpected already-existed marker:\n%s", out)
	}
}

// TestFormatPromoIssueAdmin_AlreadyExisted — флаг already_existed
// должен дать дополнительное информационное предложение.
func TestFormatPromoIssueAdmin_AlreadyExisted(t *testing.T) {
	h := &TelegramBotHandler{}
	target := &promopb.LookupUserResponse{Username: "fari3214"}
	issue := &promopb.IssuePromoResponse{
		Token:          "0011223344556677889900112233445566778899001122334455667788990011",
		PromoId:        7,
		AlreadyExisted: true,
	}
	out := h.formatPromoIssueAdmin(target, issue, "https://x", nil)

	if !strings.Contains(out, "уже был активный промо") {
		t.Errorf("expected already-existed notice in output:\n%s", out)
	}
}

// TestFormatPromoIssueAdmin_DeliveryError — если SendMessage упал
// (юзер заблокировал бота), админ должен получить warning + ссылку
// для ручной отправки.
func TestFormatPromoIssueAdmin_DeliveryError(t *testing.T) {
	h := &TelegramBotHandler{}
	target := &promopb.LookupUserResponse{TelegramId: 999} // username пустой → tg:999
	issue := &promopb.IssuePromoResponse{
		Token: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
	}
	delivery := errors.New("Forbidden: bot was blocked by the user")
	out := h.formatPromoIssueAdmin(target, issue, "https://cdn.osmonai.com/promo/p/x", delivery)

	for _, want := range []string{
		"⚠️ Промо создан, но сообщение НЕ доставлено tg:999",
		"Forbidden: bot was blocked",
		"скопировать ссылку",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n--- got ---\n%s", want, out)
		}
	}
}

// TestFormatPromoStatusAdmin — три ветки status: not-clicked, clicked
// (payment_id есть, used_at нет), paid (used_at заполнен).
func TestFormatPromoStatusAdmin(t *testing.T) {
	target := &promopb.LookupUserResponse{Username: "user1"}
	now := time.Now().Unix()
	base := promopb.GetPromoStatusResponse{
		PromoId:       42,
		Token:         "11223344aabbccddeeff00112233445566778899aabbccddeeff001122334455",
		CreatedAtUnix: now - 3600,
		ExpiresAtUnix: now + 30*24*3600,
	}

	t.Run("not clicked", func(t *testing.T) {
		st := base
		out := formatPromoStatusAdmin(target, &st)
		if !strings.Contains(out, "🕒 Не открывал") {
			t.Errorf("expected not-clicked marker:\n%s", out)
		}
	})

	t.Run("clicked but not paid", func(t *testing.T) {
		st := base
		st.PaymentId = 555
		out := formatPromoStatusAdmin(target, &st)
		if !strings.Contains(out, "🟡 Кликнул, оплата не подтверждена") {
			t.Errorf("expected clicked marker:\n%s", out)
		}
		if !strings.Contains(out, "payment_id=555") {
			t.Errorf("expected payment_id ref:\n%s", out)
		}
	})

	t.Run("paid", func(t *testing.T) {
		st := base
		st.PaymentId = 555
		st.UsedAtUnix = now - 60
		out := formatPromoStatusAdmin(target, &st)
		if !strings.Contains(out, "✅ Оплачен") {
			t.Errorf("expected paid marker:\n%s", out)
		}
	})
}
