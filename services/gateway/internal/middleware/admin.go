// Package middleware — gateway-специфичные middleware'ы.
//
// admin.go — RequireAdmin: проверяет, что JWT в запросе принадлежит
// пользователю с role='admin'. Используется на ручках /api/v1/admin/*.
package middleware

import (
	"encoding/json"
	"net/http"

	authmw "github.com/vpn/platform/pkg/middleware"
)

// RequireAdmin возвращает middleware, пропускающий запрос дальше только
// если JWT-middleware уже положил в context role='admin'. Иначе — 403.
//
// ОБЯЗАТЕЛЬНО вешать ПОСЛЕ JWTMiddleware, иначе role в контексте будет пустой
// и все запросы превратятся в 403.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := authmw.RoleFromContext(r.Context())
		if role != "admin" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":   "forbidden",
				"message": "admin role required",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}
