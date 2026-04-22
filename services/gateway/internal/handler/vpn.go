package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vpn/gateway/internal/client"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type VPNHandler struct {
	vpnClient *client.VPNClient
	logger    *zap.Logger
}

func NewVPNHandler(vpnClient *client.VPNClient, logger *zap.Logger) *VPNHandler {
	return &VPNHandler{
		vpnClient: vpnClient,
		logger:    logger,
	}
}

func (h *VPNHandler) ListServers(w http.ResponseWriter, r *http.Request) {
	activeOnly := r.URL.Query().Get("active_only") != "false"

	resp, err := h.vpnClient.ListServers(r.Context(), activeOnly)
	if err != nil {
		h.logger.Error("Failed to list servers", zap.Error(err))
		http.Error(w, "Failed to list servers", http.StatusInternalServerError)
		return
	}

	servers := make([]map[string]interface{}, 0, len(resp.Servers))
	for _, server := range resp.Servers {
		servers = append(servers, map[string]interface{}{
			"id":           server.Id,
			"name":         server.Name,
			"location":     server.Location,
			"country_code": server.CountryCode,
			"is_active":    server.IsActive,
			"load_percent": server.LoadPercent,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(servers)
}

// GetVLESSLink — GET /api/v1/vpn/link/{serverId}?device_id=iPhone[&user_id=1]
//
// device_id (query) — обязателен для проверки лимита устройств.
// Если пусто — ссылка выдаётся без учёта слотов (debug).
//
// user_id (query) — временно до JWT middleware (Этап 4). В проде будет из токена.
func (h *VPNHandler) GetVLESSLink(w http.ResponseWriter, r *http.Request) {
	serverIDStr := chi.URLParam(r, "serverId")
	serverID, err := strconv.ParseInt(serverIDStr, 10, 32)
	if err != nil {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}

	deviceID := r.URL.Query().Get("device_id")

	userID, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}

	resp, err := h.vpnClient.GetVLESSLink(r.Context(), userID, int32(serverID), deviceID)
	if err != nil {
		// Проверяем gRPC-статус на ResourceExhausted → 429 Too Many Requests.
		if st, ok := status.FromError(err); ok && st.Code() == codes.ResourceExhausted {
			h.logger.Info("device limit exceeded", zap.Int64("user_id", userID), zap.String("device", deviceID))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error":   "device_limit_exceeded",
				"message": st.Message(),
			})
			return
		}
		h.logger.Error("Failed to get VLESS link", zap.Error(err))
		http.Error(w, "Failed to get VLESS link", http.StatusInternalServerError)
		return
	}

	result := map[string]interface{}{
		"vless_link":      resp.VlessLink,
		"current_devices": resp.CurrentDevices,
		"max_devices":     resp.MaxDevices,
		"connection_id":   resp.ConnectionId,
		"server": map[string]interface{}{
			"id":           resp.Server.Id,
			"name":         resp.Server.Name,
			"location":     resp.Server.Location,
			"country_code": resp.Server.CountryCode,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// DisconnectDevice — DELETE /api/v1/vpn/devices/{connectionId}
// Удаляет запись из active_connections → слот освобождается мгновенно.
func (h *VPNHandler) DisconnectDevice(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "connectionId")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid connection ID", http.StatusBadRequest)
		return
	}

	resp, err := h.vpnClient.DisconnectDevice(r.Context(), id)
	if err != nil {
		h.logger.Error("Failed to disconnect device", zap.Error(err), zap.Int64("connection_id", id))
		http.Error(w, "Failed to disconnect device", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":       resp.Success,
		"connection_id": id,
	})
}

func (h *VPNHandler) GetActiveConnections(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}

	resp, err := h.vpnClient.GetActiveConnections(r.Context(), userID)
	if err != nil {
		h.logger.Error("Failed to get active connections", zap.Error(err))
		http.Error(w, "Failed to get connections", http.StatusInternalServerError)
		return
	}

	connections := make([]map[string]interface{}, 0, len(resp.Connections))
	for _, conn := range resp.Connections {
		connections = append(connections, map[string]interface{}{
			"id":                conn.Id,
			"server_id":         conn.ServerId,
			"server_name":       conn.ServerName,
			"device_identifier": conn.DeviceIdentifier,
			"connected_at":      conn.ConnectedAt,
			"last_seen":         conn.LastSeen,
		})
	}

	result := map[string]interface{}{
		"connections":       connections,
		"total_connections": resp.TotalConnections,
		"max_devices":       resp.MaxDevices,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
