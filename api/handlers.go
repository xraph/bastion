package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/health"
	"github.com/xraph/bastion/observability"
	"github.com/xraph/forge"
)

// Gateway provides the admin API with access to gateway components.
type Gateway interface {
	Config() bastion.Config
	RouteManager() bastion.RouteRegistry
	HealthMonitor() *health.Monitor
	Snapshot() *bastion.GatewayStats
	AccessLog() *observability.AccessLogger
	Discovery() *bastion.ServiceDiscovery
	Logger() forge.Logger
	Hub() bastion.WSBroadcaster
}

// Handlers provides REST API handlers for the gateway admin.
type Handlers struct {
	gw  Gateway
	hub *Hub
}

// NewHandlers creates new admin API handlers.
func NewHandlers(gw Gateway, hub *Hub) *Handlers {
	return &Handlers{gw: gw, hub: hub}
}

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// HandleListRoutes returns all routes.
func (h *Handlers) HandleListRoutes(ctx forge.Context) error {
	routes := h.gw.RouteManager().ListRoutes()

	source := ctx.Query("source")
	protocol := ctx.Query("protocol")

	if source != "" || protocol != "" {
		filtered := make([]*bastion.Route, 0)

		for _, r := range routes {
			if source != "" && string(r.Source) != source {
				continue
			}

			if protocol != "" && string(r.Protocol) != protocol {
				continue
			}

			filtered = append(filtered, r)
		}

		routes = filtered
	}

	return ctx.JSON(http.StatusOK, routes)
}

// HandleGetRoute returns a single route by ID.
func (h *Handlers) HandleGetRoute(ctx forge.Context) error {
	id := ctx.Param("id")

	route, ok := h.gw.RouteManager().GetRoute(id)
	if !ok {
		return ctx.JSON(http.StatusNotFound, map[string]string{"error": "route not found"})
	}

	for _, t := range route.Targets {
		t.Snapshot()
	}

	return ctx.JSON(http.StatusOK, route)
}

// HandleCreateRoute creates a new manual route.
func (h *Handlers) HandleCreateRoute(ctx forge.Context) error {
	var dto bastion.RouteDTO
	if err := json.NewDecoder(ctx.Request().Body).Decode(&dto); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}

	route := dtoToRoute(dto, h.gw.Config().BasePath)

	if err := h.gw.RouteManager().AddRoute(route); err != nil {
		return ctx.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}

	for _, target := range route.Targets {
		h.gw.HealthMonitor().Register(route.ID, target)
	}

	h.gw.AccessLog().LogAdminAction("create_route", route.ID, "success", ctx.Request())

	return ctx.JSON(http.StatusCreated, route)
}

// HandleUpdateRoute updates an existing route.
func (h *Handlers) HandleUpdateRoute(ctx forge.Context) error {
	id := ctx.Param("id")

	existing, ok := h.gw.RouteManager().GetRoute(id)
	if !ok {
		return ctx.JSON(http.StatusNotFound, map[string]string{"error": "route not found"})
	}

	if existing.Source != bastion.SourceManual {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "cannot update auto-discovered routes"})
	}

	var dto bastion.RouteDTO
	if err := json.NewDecoder(ctx.Request().Body).Decode(&dto); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}

	updated := dtoToRoute(dto, h.gw.Config().BasePath)
	updated.ID = id
	updated.Source = bastion.SourceManual

	if err := h.gw.RouteManager().UpdateRoute(updated); err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	h.gw.AccessLog().LogAdminAction("update_route", id, "success", ctx.Request())

	return ctx.JSON(http.StatusOK, updated)
}

// HandleDeleteRoute deletes a manual route.
func (h *Handlers) HandleDeleteRoute(ctx forge.Context) error {
	id := ctx.Param("id")

	route, ok := h.gw.RouteManager().GetRoute(id)
	if !ok {
		return ctx.JSON(http.StatusNotFound, map[string]string{"error": "route not found"})
	}

	if route.Source != bastion.SourceManual {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "cannot delete auto-discovered routes"})
	}

	for _, target := range route.Targets {
		h.gw.HealthMonitor().Deregister(target.ID)
	}

	if err := h.gw.RouteManager().RemoveRoute(id); err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	h.gw.AccessLog().LogAdminAction("delete_route", id, "success", ctx.Request())

	return ctx.JSON(http.StatusOK, map[string]string{"status": "deleted"})
}

// HandleEnableRoute enables a route.
func (h *Handlers) HandleEnableRoute(ctx forge.Context) error {
	id := ctx.Param("id")

	route, ok := h.gw.RouteManager().GetRoute(id)
	if !ok {
		return ctx.JSON(http.StatusNotFound, map[string]string{"error": "route not found"})
	}

	route.Enabled = true

	if err := h.gw.RouteManager().UpdateRoute(route); err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return ctx.JSON(http.StatusOK, map[string]string{"status": "enabled"})
}

// HandleDisableRoute disables a route.
func (h *Handlers) HandleDisableRoute(ctx forge.Context) error {
	id := ctx.Param("id")

	route, ok := h.gw.RouteManager().GetRoute(id)
	if !ok {
		return ctx.JSON(http.StatusNotFound, map[string]string{"error": "route not found"})
	}

	route.Enabled = false

	if err := h.gw.RouteManager().UpdateRoute(route); err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return ctx.JSON(http.StatusOK, map[string]string{"status": "disabled"})
}

// HandleListUpstreams returns all targets with health status.
func (h *Handlers) HandleListUpstreams(ctx forge.Context) error {
	routes := h.gw.RouteManager().ListRoutes()

	type UpstreamInfo struct {
		*bastion.Target
		RouteID   string `json:"routeId"`
		RoutePath string `json:"routePath"`
	}

	upstreams := make([]UpstreamInfo, 0)

	for _, r := range routes {
		for _, t := range r.Targets {
			t.Snapshot()
			upstreams = append(upstreams, UpstreamInfo{
				Target:    t,
				RouteID:   r.ID,
				RoutePath: r.Path,
			})
		}
	}

	return ctx.JSON(http.StatusOK, upstreams)
}

// HandleGetStats returns aggregated gateway statistics.
func (h *Handlers) HandleGetStats(ctx forge.Context) error {
	stats := h.gw.Snapshot()

	return ctx.JSON(http.StatusOK, stats)
}

// HandleGetRouteStats returns per-route statistics.
func (h *Handlers) HandleGetRouteStats(ctx forge.Context) error {
	stats := h.gw.Snapshot()

	return ctx.JSON(http.StatusOK, stats.RouteStats)
}

// HandleGetConfig returns the current gateway configuration.
func (h *Handlers) HandleGetConfig(ctx forge.Context) error {
	config := h.gw.Config()

	if config.TLS.ClientKeyFile != "" {
		config.TLS.ClientKeyFile = "[REDACTED]"
	}

	return ctx.JSON(http.StatusOK, config)
}

// HandleListDiscoveredServices returns all discovered services.
func (h *Handlers) HandleListDiscoveredServices(ctx forge.Context) error {
	if h.gw.Discovery() == nil {
		return ctx.JSON(http.StatusOK, []bastion.DiscoveredService{})
	}

	services := h.gw.Discovery().DiscoveredServices()

	return ctx.JSON(http.StatusOK, services)
}

// HandleRefreshDiscovery forces a FARP re-scan.
func (h *Handlers) HandleRefreshDiscovery(ctx forge.Context) error {
	if h.gw.Discovery() == nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "discovery not enabled"})
	}

	if err := h.gw.Discovery().Refresh(ctx.Context()); err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	h.gw.AccessLog().LogAdminAction("refresh_discovery", "", "success", ctx.Request())

	return ctx.JSON(http.StatusOK, map[string]string{"status": "refreshed"})
}

// HandleDebugDiscovery performs a one-shot discovery probe and returns raw
// results for diagnosing mDNS/service discovery issues.
func (h *Handlers) HandleDebugDiscovery(ctx forge.Context) error {
	if h.gw.Discovery() == nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "discovery not enabled"})
	}

	info := h.gw.Discovery().DebugDiscovery(ctx.Context())

	return ctx.JSON(http.StatusOK, info)
}

// serviceRegistrationPayload is the JSON payload for service push registration.
// It mirrors the /_farp/discovery response format.
type serviceRegistrationPayload struct {
	ServiceID      string            `json:"service_id"`
	ServiceName    string            `json:"service_name"`
	ServiceVersion string            `json:"service_version"`
	Address        string            `json:"address"`
	Port           int               `json:"port"`
	Tags           []string          `json:"tags"`
	Metadata       map[string]string `json:"metadata"`
	FARPEnabled    bool              `json:"farp_enabled"`
}

// HandleRegisterService accepts a service registration push.
// Services POST their info to this endpoint so the gateway can
// build routes from their FARP manifest.
func (h *Handlers) HandleRegisterService(ctx forge.Context) error {
	if h.gw.Discovery() == nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "discovery not enabled"})
	}

	var payload serviceRegistrationPayload
	if err := json.NewDecoder(ctx.Request().Body).Decode(&payload); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body: " + err.Error()})
	}

	if payload.ServiceName == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "service_name is required"})
	}

	if payload.Address == "" || payload.Port == 0 {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "address and port are required"})
	}

	// Ensure metadata has farp.enabled if farp_enabled is true
	metadata := payload.Metadata
	if metadata == nil {
		metadata = make(map[string]string)
	}

	if payload.FARPEnabled {
		metadata["farp.enabled"] = "true"
	}

	info := &bastion.ServiceInstanceInfo{
		ID:       payload.ServiceID,
		Name:     payload.ServiceName,
		Version:  payload.ServiceVersion,
		Address:  payload.Address,
		Port:     payload.Port,
		Tags:     payload.Tags,
		Metadata: metadata,
		Healthy:  true,
	}

	if err := h.gw.Discovery().RegisterService(ctx.Context(), info); err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	h.gw.AccessLog().LogAdminAction("register_service", payload.ServiceName, "success", ctx.Request())

	return ctx.JSON(http.StatusOK, map[string]string{
		"status":  "registered",
		"service": payload.ServiceName,
	})
}

// HandleDeregisterService removes a pushed service and its routes.
func (h *Handlers) HandleDeregisterService(ctx forge.Context) error {
	if h.gw.Discovery() == nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "discovery not enabled"})
	}

	serviceName := ctx.Param("name")
	if serviceName == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "service name is required"})
	}

	h.gw.Discovery().DeregisterService(ctx.Context(), serviceName)

	h.gw.AccessLog().LogAdminAction("deregister_service", serviceName, "success", ctx.Request())

	return ctx.JSON(http.StatusOK, map[string]string{
		"status":  "deregistered",
		"service": serviceName,
	})
}

// HandleWebSocket handles the dashboard WebSocket connection.
func (h *Handlers) HandleWebSocket(ctx forge.Context) error {
	if h.hub == nil {
		return ctx.JSON(http.StatusServiceUnavailable, map[string]string{"error": "realtime not enabled"})
	}

	conn, err := wsUpgrader.Upgrade(ctx.Response(), ctx.Request(), nil)
	if err != nil {
		h.gw.Logger().Error("websocket upgrade failed", forge.F("error", err))

		return nil //nolint:nilerr // Upgrade already wrote response
	}

	client := NewClient(h.hub, conn)
	h.hub.register <- client
	client.Start()

	return nil
}

// dtoToRoute converts a RouteDTO to a Route.
func dtoToRoute(dto bastion.RouteDTO, basePath string) *bastion.Route {
	path := strings.TrimRight(basePath, "/") + dto.Path

	targets := make([]*bastion.Target, 0, len(dto.Targets))
	for _, td := range dto.Targets {
		t := &bastion.Target{
			ID:       "target-" + strings.ReplaceAll(td.URL, "://", "-"),
			URL:      td.URL,
			Weight:   td.Weight,
			Healthy:  true,
			Tags:     td.Tags,
			Metadata: td.Metadata,
			TLS:      td.TLS,
		}

		if t.Weight <= 0 {
			t.Weight = 1
		}

		targets = append(targets, t)
	}

	protocol := dto.Protocol
	if protocol == "" {
		protocol = bastion.ProtocolHTTP
	}

	return &bastion.Route{
		Path:           path,
		Methods:        dto.Methods,
		Targets:        targets,
		StripPrefix:    dto.StripPrefix,
		AddPrefix:      dto.AddPrefix,
		RewritePath:    dto.RewritePath,
		Headers:        dto.Headers,
		Protocol:       protocol,
		Source:         bastion.SourceManual,
		Priority:       dto.Priority + 100,
		Enabled:        dto.Enabled,
		Retry:          dto.Retry,
		Timeout:        dto.Timeout,
		RateLimit:      dto.RateLimit,
		Auth:           dto.Auth,
		CircuitBreaker: dto.CircuitBreaker,
		Cache:          dto.Cache,
		TrafficPolicy:  dto.TrafficPolicy,
		Transform:      dto.Transform,
		Metadata:       dto.Metadata,
	}
}
