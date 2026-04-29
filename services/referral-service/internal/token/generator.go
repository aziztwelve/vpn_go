// Package token генерирует короткие случайные токены для реферальных ссылок.
//
// Формат: 8 символов из base62-алфавита (a-z, A-Z, 0-9). Это даёт
// 62^8 ≈ 2.18 * 10^14 вариантов — коллизии практически невозможны при
// миллионах юзеров. URL-safe, читается, помещается в Telegram start_param
// (max 64 байта).
package token

import (
	"crypto/rand"
	"errors"
	"math/big"
)

const (
	defaultLength = 8
	alphabet      = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

// Generate возвращает случайный токен длины n (n=0 → defaultLength).
// Использует crypto/rand — пригодно для anti-abuse (нельзя угадать).
func Generate(n int) (string, error) {
	if n <= 0 {
		n = defaultLength
	}
	mod := big.NewInt(int64(len(alphabet)))
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		idx, err := rand.Int(rand.Reader, mod)
		if err != nil {
			return "", err
		}
		out[i] = alphabet[idx.Int64()]
	}
	return string(out), nil
}

// IsValid проверяет что строка соответствует формату токена:
// все символы из base62-алфавита, длина 4..32. Используется для
// быстрой валидации входящего ref_token перед запросом в БД.
func IsValid(s string) bool {
	if len(s) < 4 || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if !isBase62(byte(c)) {
			return false
		}
	}
	return true
}

func isBase62(c byte) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}

// ErrEmptyAlphabet — защита от случайного "обнуления" алфавита.
var ErrEmptyAlphabet = errors.New("empty alphabet")
