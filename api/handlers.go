package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/xraph/farp"
	"github.com/xraph/forge"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/admin"
	"github.com/xraph/bastion/health"
	"github.com/xraph/bastion/observability"
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
	svc *admin.Service
}

// NewHandlers creates new admin API handlers.
func NewHandlers(gw Gateway, hub *Hub, svc *admin.Service) *Handlers {
	return &Handlers{gw: gw, hub: hub, svc: svc}
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

// writeAdminError maps an admin service error to the REST status and body
// the handlers have always used.
func writeAdminError(ctx forge.Context, err error, discovered string) error {
	var ve *admin.ValidationError
	switch {
	case errors.As(err, &ve):
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": ve.Error(), "field": ve.Field})
	case errors.Is(err, admin.ErrNotFound):
		return ctx.JSON(http.StatusNotFound, map[string]string{"error": "route not found"})
	case errors.Is(err, admin.ErrNotManual):
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": discovered})
	case errors.Is(err, admin.ErrConflict):
		return ctx.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}

// HandleCreateRoute creates a new manual route.
func (h *Handlers) HandleCreateRoute(ctx forge.Context) error {
	var dto bastion.RouteDTO
	if err := json.NewDecoder(ctx.Request().Body).Decode(&dto); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}

	route, err := h.svc.CreateRoute(dto)
	if err != nil {
		return writeAdminError(ctx, err, "")
	}

	h.gw.AccessLog().LogAdminAction("create_route", route.ID, "success", ctx.Request())

	return ctx.JSON(http.StatusCreated, route)
}

// HandleUpdateRoute updates an existing route.
func (h *Handlers) HandleUpdateRoute(ctx forge.Context) error {
	id := ctx.Param("id")

	var dto bastion.RouteDTO
	if err := json.NewDecoder(ctx.Request().Body).Decode(&dto); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}

	updated, err := h.svc.UpdateRoute(id, dto)
	if err != nil {
		return writeAdminError(ctx, err, "cannot update auto-discovered routes")
	}

	h.gw.AccessLog().LogAdminAction("update_route", id, "success", ctx.Request())

	return ctx.JSON(http.StatusOK, updated)
}

// HandleDeleteRoute deletes a manual route.
func (h *Handlers) HandleDeleteRoute(ctx forge.Context) error {
	id := ctx.Param("id")

	if err := h.svc.DeleteRoute(id); err != nil {
		return writeAdminError(ctx, err, "cannot delete auto-discovered routes")
	}

	h.gw.AccessLog().LogAdminAction("delete_route", id, "success", ctx.Request())

	return ctx.JSON(http.StatusOK, map[string]string{"status": "deleted"})
}

// HandleEnableRoute enables a route.
func (h *Handlers) HandleEnableRoute(ctx forge.Context) error {
	return h.setEnabled(ctx, true, "enabled")
}

// HandleDisableRoute disables a route.
func (h *Handlers) HandleDisableRoute(ctx forge.Context) error {
	return h.setEnabled(ctx, false, "disabled")
}

func (h *Handlers) setEnabled(ctx forge.Context, enabled bool, status string) error {
	id := ctx.Param("id")

	if _, err := h.svc.SetEnabled(id, enabled); err != nil {
		return writeAdminError(ctx, err, "cannot change auto-discovered routes: the next discovery update would undo it")
	}

	h.gw.AccessLog().LogAdminAction(status+"_route", id, "success", ctx.Request())

	return ctx.JSON(http.StatusOK, map[string]string{"status": status})
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

// farpV1PushPayload is the FARP v1 push protocol payload (spec section 17.4).
// Services POST this to /_farp/v1/register.
type farpV1PushPayload struct {
	Instance farpV1Instance  `json:"instance"`
	Manifest json.RawMessage `json:"manifest,omitempty"`
}

// farpV1Instance represents a service instance in the FARP v1 push protocol.
type farpV1Instance struct {
	ID             string            `json:"id"`
	ServiceName    string            `json:"service_name"`
	ServiceVersion string            `json:"service_version"`
	Address        string            `json:"address"`
	Port           int               `json:"port"`
	Tags           []string          `json:"tags"`
	Metadata       map[string]string `json:"metadata"`
	Status         string            `json:"status"`
}

// HandleFARPRegister handles the FARP v1 push registration protocol.
// POST /_farp/v1/register — accepts {instance: {...}, manifest?: {...}}
// The gateway fetches the full manifest from the service's /_farp/manifest
// endpoint if not included in the payload.
func (h *Handlers) HandleFARPRegister(ctx forge.Context) error {
	if h.gw.Discovery() == nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "discovery not enabled"})
	}

	var payload farpV1PushPayload
	if err := json.NewDecoder(ctx.Request().Body).Decode(&payload); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body: " + err.Error()})
	}

	inst := payload.Instance

	if inst.ServiceName == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "instance.service_name is required"})
	}

	if inst.Address == "" || inst.Port == 0 {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "instance.address and instance.port are required"})
	}

	// Ensure metadata has farp.enabled
	metadata := inst.Metadata
	if metadata == nil {
		metadata = make(map[string]string)
	}
	metadata["farp.enabled"] = "true"

	// Build the manifest URL from the instance address so the discovery
	// manager can fetch the manifest when processing the registration.
	if _, ok := metadata["farp.manifest"]; !ok {
		metadata["farp.manifest"] = fmt.Sprintf("http://%s:%d/_farp/manifest", inst.Address, inst.Port)
	}

	info := &bastion.ServiceInstanceInfo{
		ID:       inst.ID,
		Name:     inst.ServiceName,
		Version:  inst.ServiceVersion,
		Address:  inst.Address,
		Port:     inst.Port,
		Tags:     inst.Tags,
		Metadata: metadata,
		Healthy:  inst.Status == "" || inst.Status == "healthy",
	}

	// If the payload includes an inline manifest, decode and pre-set it
	// so the discovery manager uses it instead of fetching via HTTP.
	if len(payload.Manifest) > 0 {
		var manifest farp.SchemaManifest
		if err := json.Unmarshal(payload.Manifest, &manifest); err == nil {
			h.gw.Discovery().SetManifest(inst.ServiceName, &manifest)
		}
	}

	if err := h.gw.Discovery().RegisterService(ctx.Context(), info); err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	h.gw.AccessLog().LogAdminAction("farp_register", inst.ServiceName, "success", ctx.Request())

	return ctx.JSON(http.StatusOK, map[string]any{
		"status":   "registered",
		"service":  inst.ServiceName,
		"instance": inst.ID,
	})
}

// farpV1HeartbeatRequest is the optional JSON body for the FARP v1 heartbeat.
type farpV1HeartbeatRequest struct {
	Status         string `json:"status"`
	RoutesChecksum string `json:"routes_checksum,omitempty"`
}

// HandleFARPHeartbeat handles FARP v1 heartbeat protocol.
// PUT /_farp/v1/heartbeat/:id
//
// Returns the gateway's known routes_checksum so the service SDK can
// detect mismatches and trigger re-registration for reconciliation.
func (h *Handlers) HandleFARPHeartbeat(ctx forge.Context) error {
	if h.gw.Discovery() == nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "discovery not enabled"})
	}

	instanceID := ctx.Param("id")

	var hb farpV1HeartbeatRequest
	_ = json.NewDecoder(ctx.Request().Body).Decode(&hb) // allow empty body

	gwChecksum, schemasApplied := h.gw.Discovery().GetInstanceChecksum(instanceID)

	return ctx.JSON(http.StatusOK, map[string]any{
		"status":          "ok",
		"instance":        instanceID,
		"routes_checksum": gwChecksum,
		"schemas_applied": schemasApplied,
	})
}

// HandleFARPDeregister handles FARP v1 deregistration.
// DELETE /_farp/v1/deregister/:id
func (h *Handlers) HandleFARPDeregister(ctx forge.Context) error {
	if h.gw.Discovery() == nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "discovery not enabled"})
	}

	instanceID := ctx.Param("id")
	if instanceID == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "instance id is required"})
	}

	// Instance-level deregistration: we need to find which service this
	// instance belongs to. For now, iterate discovered services.
	// TODO: Add instance-level deregistration to discovery Manager.

	return ctx.JSON(http.StatusOK, map[string]any{
		"status":   "deregistered",
		"instance": instanceID,
	})
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
