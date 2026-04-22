// Package middleware — HTTP-middleware общего назначения для gateway-слоя.
//
// JWTMiddleware — проверка `Authorization: Bearer <jwt>` заголовка:
//   - подпись сверяется с секретом (общий с Auth Service)
//   - проверяется exp (срок действия)
//   - user_id и role достаются из claims и кладутся в context
//
// Handler'ы читают user_id через UserIDFromContext(ctx), а не из query-параметров,
// что закрывает тривиальную уязвимость "подделай ?user_id=99".
package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// ctxKey — приватный тип чтобы избежать коллизий с другими context.Value.
type ctxKey string

const (
	ctxKeyUserID ctxKey = "userID"
	ctxKeyRole   ctxKey = "role"
)

// JWTMiddleware возвращает chi/net-http совместимую middleware-функцию.
// secret — тот же JWT_SECRET, что используется Auth Service для подписи.
func JWTMiddleware(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if auth == "" {
				writeAuthError(w, http.StatusUnauthorized, "missing_token", "Authorization header required")
				return
			}
			if !strings.HasPrefix(auth, "Bearer ") {
				writeAuthError(w, http.StatusUnauthorized, "invalid_scheme", "Authorization must be Bearer")
				return
			}
			tokenString := strings.TrimPrefix(auth, "Bearer ")

			claims := jwt.MapClaims{}
			token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, errors.New("unexpected signing method")
				}
				return []byte(secret), nil
			})
			if err != nil {
				// Различаем expired vs прочие ошибки — клиенту нужен спец-код
				// чтобы триггернуть re-login.
				if errors.Is(err, jwt.ErrTokenExpired) {
					writeAuthError(w, http.StatusUnauthorized, "token_expired", "JWT token has expired")
					return
				}
				writeAuthError(w, http.StatusUnauthorized, "invalid_signature", "invalid token: "+err.Error())
				return
			}
			if !token.Valid {
				writeAuthError(w, http.StatusUnauthorized, "invalid_token", "token is not valid")
				return
			}

			// В claims user_id лежит как float64 (JSON-число).
			uidRaw, ok := claims["user_id"]
			if !ok {
				writeAuthError(w, http.StatusUnauthorized, "missing_user_id", "user_id not in token")
				return
			}
			userID := int64FromClaim(uidRaw)
			if userID == 0 {
				writeAuthError(w, http.StatusUnauthorized, "invalid_user_id", "invalid user_id in token")
				return
			}
			role, _ := claims["role"].(string)

			ctx := r.Context()
			ctx = context.WithValue(ctx, ctxKeyUserID, userID)
			ctx = context.WithValue(ctx, ctxKeyRole, role)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserIDFromContext достаёт user_id, который middleware положил в контекст.
// Возвращает (0, false) если контекст не прошёл через middleware.
func UserIDFromContext(ctx context.Context) (int64, bool) {
	v, ok := ctx.Value(ctxKeyUserID).(int64)
	return v, ok
}

// RoleFromContext — аналогично для роли. "" если отсутствует.
func RoleFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRole).(string); ok {
		return v
	}
	return ""
}

func int64FromClaim(v interface{}) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case json.Number:
		i, _ := x.Int64()
		return i
	}
	return 0
}

func writeAuthError(w http.ResponseWriter, code int, errKey, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   errKey,
		"message": msg,
	})
}
