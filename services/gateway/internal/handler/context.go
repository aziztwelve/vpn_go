package handler

import (
	"encoding/json"
	"net/http"

	authmw "github.com/vpn/platform/pkg/middleware"
)

// userIDFromRequest извлекает user_id из context (положен JWT middleware).
// Если middleware не отработал (например, ручку забыли защитить) — пишет
// 401 и возвращает (0, false). Вызывающий handler должен сразу `return`.
func userIDFromRequest(w http.ResponseWriter, r *http.Request) (int64, bool) {
	uid, ok := authmw.UserIDFromContext(r.Context())
	if !ok || uid == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":   "unauthorized",
			"message": "user_id missing from context (middleware not applied?)",
		})
		return 0, false
	}
	return uid, true
}
