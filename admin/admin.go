// Package admin owns the gateway's operator writes and the read models that
// the REST API and the dashboard contract share. Both callers go through it,
// so the manual-route priority offset and the proxy base path are applied in
// one place, and a route loaded and saved unchanged is left unchanged.
package admin

import (
	"errors"
	"sync"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/health"
)

// ManualPriorityOffset is added to every manual route's priority so manual
// routes sort above discovered ones at the same specificity. Operators never
// see it: the service adds it on the way in and removes it on the way out.
const ManualPriorityOffset = 100

// Deps is what the service needs from the gateway.
type Deps struct {
	// Routes is the live route table. Required.
	Routes bastion.RouteRegistry
	// Health, when set, has targets a write removes deregistered from it.
	Health *health.Monitor
	// Breakers, when set, supplies circuit state for read models and has the
	// breakers of removed targets dropped.
	Breakers bastion.CircuitControl
	// BasePath is bastion's Config.BasePath: the prefix every manual route's
	// path is served under.
	BasePath string
	// Persisted reports whether a route store is wired. Nil means nothing
	// written through the service survives a restart.
	Persisted func() bool
}

// Service answers operator reads and performs operator writes.
type Service struct {
	d Deps

	// mu serialises writes. A conflict check and the write it guards must
	// see the same route table, or two concurrent creates both pass.
	mu sync.Mutex
}

// New builds a Service.
func New(d Deps) (*Service, error) {
	if d.Routes == nil {
		return nil, errors.New("admin: Routes is required")
	}

	return &Service{d: d}, nil
}

func (s *Service) persisted() bool {
	return s.d.Persisted != nil && s.d.Persisted()
}
