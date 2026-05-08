// Тесты на error-pages /promo/p/{token} (writePromoError) — на уровне
// HTTP-рендеринга. Полный flow (Redeem → CreateInvoice → 302) покрывается
// интеграционным smoke-тестом на staging (см. docs/tasks/...).
package handler

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWritePromoError_BasicFields — статус-код, content-type и наличие
// title+body в HTML.
func TestWritePromoError_BasicFields(t *testing.T) {
	w := httptest.NewRecorder()
	writePromoError(w, 404, "Не найдено", "Описание ошибки")

	if w.Code != 404 {
		t.Errorf("status code = %d, want 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}

	body := w.Body.String()
	for _, want := range []string{
		"<title>Не найдено — MaydaVPN</title>",
		"<h1>Не найдено</h1>",
		"<p>Описание ошибки</p>",
		"https://t.me/maydavpn_support",
		"viewport",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

// TestWritePromoError_HTMLEscape — тег/spec символы в title/body
// должны эскейпиться, чтобы не было XSS даже от "своих" логов.
func TestWritePromoError_HTMLEscape(t *testing.T) {
	w := httptest.NewRecorder()
	writePromoError(w, 400, "<script>alert(1)</script>", `bad & "stuff"`)

	body := w.Body.String()
	if strings.Contains(body, "<script>alert") {
		t.Errorf("XSS in title: %s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("script not escaped:\n%s", body)
	}
	if !strings.Contains(body, "&amp;") || !strings.Contains(body, "&quot;") {
		t.Errorf("amp/quot not escaped:\n%s", body)
	}
}

// TestEscapeHTML — единичный тест на helper. Покрытие: <, >, &, ", обычные символы.
func TestEscapeHTML(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{"<tag>", "&lt;tag&gt;"},
		{`a & b`, "a &amp; b"},
		{`"x"`, "&quot;x&quot;"},
		{"привет", "привет"},
	}
	for _, c := range cases {
		if got := escapeHTML(c.in); got != c.want {
			t.Errorf("escapeHTML(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
