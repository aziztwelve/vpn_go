package repository

import (
	"reflect"
	"testing"
)

// TestServerNamesRoundTrip — pure-функции encode/decode SNI-пула.
// Контракт: любой валидный []string после marshalServerNames →
// scanServerNames должен давать тот же набор значений.
//
// История: ранее marshalServerNames возвращал []byte, и pgx5 в колонку
// JSONB кодировал его как bytea + неявный cast text→jsonb, из-за чего
// иногда payload оборачивался в text-string-jsonb (получались double-nested
// массивы вида `[["apple.com"]]`). Сейчас возвращаем string и в SQL стоит
// `$N::jsonb` cast — этот тест защищает от регресса.
func TestServerNamesRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"single SNI", []string{"github.com"}, []string{"github.com"}},
		{"4-element pool", []string{"a.com", "b.com", "c.com", "d.com"}, []string{"a.com", "b.com", "c.com", "d.com"}},
		{"unicode/punycode", []string{"xn--e1afmkfd.xn--p1ai", "m.vk.com"}, []string{"xn--e1afmkfd.xn--p1ai", "m.vk.com"}},
		{"with subdomain dots", []string{"mail.hohlov.tech", "www.max.ru"}, []string{"mail.hohlov.tech", "www.max.ru"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := marshalServerNames(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got, err := scanServerNames([]byte(raw))
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("round-trip: want %v, got %v (raw=%s)", tc.want, got, raw)
			}
		})
	}
}

// TestMarshalServerNames_NilAndEmpty — NULL/nil/empty-slice одинаково
// отдаются как "[]" (валидный JSONB-массив), а не как "null"/"".
// БД-колонка `server_names JSONB NOT NULL` отвергнет null payload,
// поэтому контракт критичен.
func TestMarshalServerNames_NilAndEmpty(t *testing.T) {
	cases := []struct {
		name string
		in   []string
	}{
		{"nil slice", nil},
		{"empty slice", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := marshalServerNames(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if got != "[]" {
				t.Errorf("want %q, got %q", "[]", got)
			}
		})
	}
}

// TestScanServerNames_RejectsDoubleNested — главный регресс-тест.
// Если в БД лежит `[["apple.com"]]` (артефакт старой бажной записи),
// scanServerNames должен либо вернуть ошибку, либо НЕ молча выдавать
// мусор. Текущая реализация Unmarshal в []string упадёт с decode error —
// это правильное поведение (пусть caller увидит и зашумит warn).
func TestScanServerNames_RejectsDoubleNested(t *testing.T) {
	doubleNested := []byte(`[["apple.com"]]`)
	_, err := scanServerNames(doubleNested)
	if err == nil {
		t.Errorf("double-nested array must error, got nil — regression to old bug")
	}
}

// TestScanServerNames_EmptyPayload — пустой payload не должен паниковать,
// возвращает nil без ошибки (caller увидит len==0).
func TestScanServerNames_EmptyPayload(t *testing.T) {
	got, err := scanServerNames(nil)
	if err != nil {
		t.Errorf("nil payload: unexpected err: %v", err)
	}
	if got != nil {
		t.Errorf("nil payload: want nil slice, got %v", got)
	}
}
