package admin

import (
	"errors"
	"fmt"

	bastion "github.com/xraph/bastion"
)

var (
	// ErrNotFound is returned for a route id the table does not hold.
	ErrNotFound = errors.New("route not found")
	// ErrNotManual is wrapped by SourceError.
	ErrNotManual = errors.New("route is not manual")
	// ErrConflict is wrapped by ConflictError.
	ErrConflict = errors.New("route conflicts with an existing route")
)

// ValidationError describes bad operator input. Field names the input it is
// about, so a form can put the message beside it.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

// SourceError refuses a write to a route the service does not manage: FARP
// and discovery rebuild their routes on every update, so a change made here
// would silently revert.
type SourceError struct {
	ID     string
	Source bastion.RouteSource
}

func (e *SourceError) Error() string {
	return fmt.Sprintf("route %q comes from %s; its next update would undo any change made here", e.ID, e.Source)
}

func (e *SourceError) Unwrap() error { return ErrNotManual }

// ConflictError refuses a manual route that would serve the same path and an
// overlapping method as another manual route.
type ConflictError struct {
	Path    string
	OtherID string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("route %q already serves %s for an overlapping method", e.OtherID, e.Path)
}

func (e *ConflictError) Unwrap() error { return ErrConflict }
