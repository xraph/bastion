package routing

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	bastion "github.com/xraph/bastion"
)

// Manager manages the dynamic route table with thread-safe operations.
type Manager struct {
	// routeTable is swapped atomically for lock-free reads
	routeTable atomic.Pointer[routeTable]

	// mu protects writes (add/remove/update)
	mu sync.Mutex

	// Event listeners
	listenersMu sync.RWMutex
	listeners   []func(bastion.RouteEvent)
}

type routeTable struct {
	routes  []*bastion.Route            // sorted by priority descending
	byID    map[string]*bastion.Route   // quick lookup by ID
	byPath  map[string][]*bastion.Route // index by path for conflict detection
	version int64
}

func newRouteTable() *routeTable {
	return &routeTable{
		routes:  make([]*bastion.Route, 0),
		byID:    make(map[string]*bastion.Route),
		byPath:  make(map[string][]*bastion.Route),
		version: 0,
	}
}

// clone creates a deep copy of the route table for atomic swap.
func (rt *routeTable) clone() *routeTable {
	newRT := &routeTable{
		routes:  make([]*bastion.Route, len(rt.routes)),
		byID:    make(map[string]*bastion.Route, len(rt.byID)),
		byPath:  make(map[string][]*bastion.Route),
		version: rt.version + 1,
	}

	copy(newRT.routes, rt.routes)

	for k, v := range rt.byID {
		newRT.byID[k] = v
	}

	for k, v := range rt.byPath {
		newSlice := make([]*bastion.Route, len(v))
		copy(newSlice, v)
		newRT.byPath[k] = newSlice
	}

	return newRT
}

// NewManager creates a new route manager.
func NewManager() *Manager {
	rm := &Manager{}
	rm.routeTable.Store(newRouteTable())

	return rm
}

// OnRouteChange registers a listener for route events.
func (rm *Manager) OnRouteChange(fn func(bastion.RouteEvent)) {
	rm.listenersMu.Lock()
	defer rm.listenersMu.Unlock()

	rm.listeners = append(rm.listeners, fn)
}

// AddRoute adds a new route to the table.
func (rm *Manager) AddRoute(route *bastion.Route) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	current := rm.routeTable.Load()

	// Check for duplicate ID
	if _, exists := current.byID[route.ID]; exists {
		return fmt.Errorf("route with ID %q already exists", route.ID)
	}

	// Assign ID if empty
	if route.ID == "" {
		route.ID = uuid.New().String()
	}

	now := time.Now()
	route.CreatedAt = now
	route.UpdatedAt = now

	// Clone and add
	newTable := current.clone()
	newTable.routes = append(newTable.routes, route)
	newTable.byID[route.ID] = route
	newTable.byPath[route.Path] = append(newTable.byPath[route.Path], route)

	// Sort by priority (descending)
	sort.Slice(newTable.routes, func(i, j int) bool {
		if newTable.routes[i].Priority != newTable.routes[j].Priority {
			return newTable.routes[i].Priority > newTable.routes[j].Priority
		}
		// Manual routes take precedence over auto-discovered
		return sourceWeight(newTable.routes[i].Source) > sourceWeight(newTable.routes[j].Source)
	})

	// Atomic swap
	rm.routeTable.Store(newTable)

	// Emit event
	rm.emit(bastion.RouteEvent{
		Type:      bastion.RouteEventAdded,
		Route:     route,
		Timestamp: now,
	})

	return nil
}

// RemoveRoute removes a route by ID.
func (rm *Manager) RemoveRoute(id string) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	current := rm.routeTable.Load()

	route, exists := current.byID[id]
	if !exists {
		return fmt.Errorf("route %q not found", id)
	}

	newTable := current.clone()
	delete(newTable.byID, id)

	// Remove from routes slice
	for i, r := range newTable.routes {
		if r.ID == id {
			newTable.routes = append(newTable.routes[:i], newTable.routes[i+1:]...)

			break
		}
	}

	// Remove from path index
	if pathRoutes, ok := newTable.byPath[route.Path]; ok {
		for i, r := range pathRoutes {
			if r.ID == id {
				newTable.byPath[route.Path] = append(pathRoutes[:i], pathRoutes[i+1:]...)

				break
			}
		}

		if len(newTable.byPath[route.Path]) == 0 {
			delete(newTable.byPath, route.Path)
		}
	}

	rm.routeTable.Store(newTable)

	rm.emit(bastion.RouteEvent{
		Type:      bastion.RouteEventRemoved,
		Route:     route,
		Timestamp: time.Now(),
	})

	return nil
}

// UpdateRoute updates an existing route.
func (rm *Manager) UpdateRoute(route *bastion.Route) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	current := rm.routeTable.Load()

	existing, exists := current.byID[route.ID]
	if !exists {
		return fmt.Errorf("route %q not found", route.ID)
	}

	route.CreatedAt = existing.CreatedAt
	route.UpdatedAt = time.Now()
	route.Version = existing.Version + 1

	newTable := current.clone()

	// Replace in routes slice
	for i, r := range newTable.routes {
		if r.ID == route.ID {
			newTable.routes[i] = route

			break
		}
	}

	// Update path index if path changed
	if existing.Path != route.Path {
		// Remove from old path
		if pathRoutes, ok := newTable.byPath[existing.Path]; ok {
			for i, r := range pathRoutes {
				if r.ID == route.ID {
					newTable.byPath[existing.Path] = append(pathRoutes[:i], pathRoutes[i+1:]...)

					break
				}
			}
		}

		// Add to new path
		newTable.byPath[route.Path] = append(newTable.byPath[route.Path], route)
	} else {
		// Update in-place in path index
		if pathRoutes, ok := newTable.byPath[route.Path]; ok {
			for i, r := range pathRoutes {
				if r.ID == route.ID {
					pathRoutes[i] = route

					break
				}
			}
		}
	}

	newTable.byID[route.ID] = route

	// Re-sort
	sort.Slice(newTable.routes, func(i, j int) bool {
		if newTable.routes[i].Priority != newTable.routes[j].Priority {
			return newTable.routes[i].Priority > newTable.routes[j].Priority
		}

		return sourceWeight(newTable.routes[i].Source) > sourceWeight(newTable.routes[j].Source)
	})

	rm.routeTable.Store(newTable)

	rm.emit(bastion.RouteEvent{
		Type:      bastion.RouteEventUpdated,
		Route:     route,
		Timestamp: time.Now(),
	})

	return nil
}

// GetRoute returns a route by ID.
func (rm *Manager) GetRoute(id string) (*bastion.Route, bool) {
	table := rm.routeTable.Load()
	route, ok := table.byID[id]

	return route, ok
}

// ListRoutes returns all routes.
func (rm *Manager) ListRoutes() []*bastion.Route {
	table := rm.routeTable.Load()
	result := make([]*bastion.Route, len(table.routes))
	copy(result, table.routes)

	return result
}

// MatchRoute finds the best matching route for a request path and method.
func (rm *Manager) MatchRoute(path, method string) *bastion.Route {
	table := rm.routeTable.Load()

	// Routes are sorted by priority, first match wins
	for _, route := range table.routes {
		if !route.Enabled {
			continue
		}

		if matchPath(route.Path, path) {
			if len(route.Methods) == 0 || containsMethod(route.Methods, method) {
				return route
			}
		}
	}

	return nil
}

// RouteCount returns the number of routes.
func (rm *Manager) RouteCount() int {
	table := rm.routeTable.Load()

	return len(table.routes)
}

// RemoveBySource removes all routes from a specific source.
func (rm *Manager) RemoveBySource(source bastion.RouteSource) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	current := rm.routeTable.Load()
	newTable := newRouteTable()
	newTable.version = current.version + 1

	var removed []*bastion.Route

	for _, r := range current.routes {
		if r.Source == source {
			removed = append(removed, r)

			continue
		}

		newTable.routes = append(newTable.routes, r)
		newTable.byID[r.ID] = r
		newTable.byPath[r.Path] = append(newTable.byPath[r.Path], r)
	}

	rm.routeTable.Store(newTable)

	for _, r := range removed {
		rm.emit(bastion.RouteEvent{
			Type:      bastion.RouteEventRemoved,
			Route:     r,
			Timestamp: time.Now(),
		})
	}
}

// RemoveByServiceName removes all routes for a service.
func (rm *Manager) RemoveByServiceName(serviceName string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	current := rm.routeTable.Load()
	newTable := newRouteTable()
	newTable.version = current.version + 1

	var removed []*bastion.Route

	for _, r := range current.routes {
		if r.ServiceName == serviceName {
			removed = append(removed, r)

			continue
		}

		newTable.routes = append(newTable.routes, r)
		newTable.byID[r.ID] = r
		newTable.byPath[r.Path] = append(newTable.byPath[r.Path], r)
	}

	rm.routeTable.Store(newTable)

	for _, r := range removed {
		rm.emit(bastion.RouteEvent{
			Type:      bastion.RouteEventRemoved,
			Route:     r,
			Timestamp: time.Now(),
		})
	}
}

func (rm *Manager) emit(event bastion.RouteEvent) {
	rm.listenersMu.RLock()
	listeners := make([]func(bastion.RouteEvent), len(rm.listeners))
	copy(listeners, rm.listeners)
	rm.listenersMu.RUnlock()

	for _, fn := range listeners {
		go fn(event)
	}
}

// matchPath checks if a request path matches a route pattern.
// Supports exact match, prefix match with /*, and path parameters with :param.
func matchPath(pattern, path string) bool {
	// Exact match
	if pattern == path {
		return true
	}

	// Wildcard suffix match
	if strings.HasSuffix(pattern, "/*") {
		prefix := strings.TrimSuffix(pattern, "/*")
		if strings.HasPrefix(path, prefix+"/") || path == prefix {
			return true
		}
	}

	// Wildcard prefix match (catch-all)
	if pattern == "/*" || pattern == "/" {
		return true
	}

	// Path parameter matching
	patternParts := strings.Split(strings.Trim(pattern, "/"), "/")
	pathParts := strings.Split(strings.Trim(path, "/"), "/")

	if len(patternParts) != len(pathParts) {
		// Check if last pattern part is wildcard
		if len(patternParts) > 0 && patternParts[len(patternParts)-1] == "*" {
			if len(pathParts) >= len(patternParts)-1 {
				patternParts = patternParts[:len(patternParts)-1]
				pathParts = pathParts[:len(patternParts)]
			} else {
				return false
			}
		} else {
			return false
		}
	}

	for i, pp := range patternParts {
		if strings.HasPrefix(pp, ":") {
			continue // Parameter matches anything
		}

		if pp == "*" {
			continue // Wildcard matches anything
		}

		if pp != pathParts[i] {
			return false
		}
	}

	return true
}

func containsMethod(methods []string, method string) bool {
	method = strings.ToUpper(method)

	for _, m := range methods {
		if strings.ToUpper(m) == method {
			return true
		}
	}

	return false
}

func sourceWeight(source bastion.RouteSource) int {
	switch source {
	case bastion.SourceManual:
		return 3
	case bastion.SourceFARP:
		return 2
	case bastion.SourceDiscovery:
		return 1
	default:
		return 0
	}
}
