package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vpn/gateway/internal/client"
	"go.uber.org/zap"
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

func (h *VPNHandler) GetVLESSLink(w http.ResponseWriter, r *http.Request) {
	serverIDStr := chi.URLParam(r, "serverId")
	serverID, err := strconv.ParseInt(serverIDStr, 10, 32)
	if err != nil {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}

	// TODO: Get user ID from JWT token
	userID := int64(1) // Mock for now

	resp, err := h.vpnClient.GetVLESSLink(r.Context(), userID, int32(serverID))
	if err != nil {
		h.logger.Error("Failed to get VLESS link", zap.Error(err))
		http.Error(w, "Failed to get VLESS link", http.StatusInternalServerError)
		return
	}

	result := map[string]interface{}{
		"vless_link": resp.VlessLink,
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

func (h *VPNHandler) GetActiveConnections(w http.ResponseWriter, r *http.Request) {
	// TODO: Get user ID from JWT token
	userID := int64(1) // Mock for now

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
