package contract

import (
	"errors"
	"strings"

	"github.com/xraph/forge"
	"github.com/xraph/forge/extensions/dashboard/contract"

	"github.com/xraph/bastion/admin"
)

// mapError turns an admin error into a *contract.Error. Anything unknown is
// CodeInternal with a generic message; its own text never reaches the client.
func mapError(err error) error {
	if err == nil {
		return nil
	}

	var (
		ve *admin.ValidationError
		se *admin.SourceError
		ce *admin.ConflictError
	)

	switch {
	case errors.As(err, &ve):
		return &contract.Error{Code: contract.CodeBadRequest, Message: ve.Message, Details: map[string]any{"field": ve.Field}}
	case errors.Is(err, admin.ErrNotFound):
		return &contract.Error{Code: contract.CodeNotFound, Message: "route not found"}
	case errors.As(err, &se):
		// forge has no FAILED_PRECONDITION. CONFLICT with a reason lets the
		// page tell "not yours to change" from "clashes with another route".
		return &contract.Error{Code: contract.CodeConflict, Message: se.Error(), Details: map[string]any{"reason": "source", "source": string(se.Source)}}
	case errors.As(err, &ce):
		return &contract.Error{Code: contract.CodeConflict, Message: ce.Error(), Details: map[string]any{"reason": "duplicate", "routeId": ce.OtherID}}
	default:
		return &contract.Error{Code: contract.CodeInternal, Message: "an internal error occurred"}
	}
}

// mapError maps like the package-level one and logs the CodeInternal case.
func (d Deps) mapError(intent string, err error) error {
	mapped := mapError(err)
	if d.Logger == nil || mapped == nil {
		return mapped
	}

	var ce *contract.Error
	if errors.As(mapped, &ce) && ce.Code == contract.CodeInternal {
		d.Logger.Error("bastion/contract: internal error answering intent", forge.F("intent", intent), forge.F("error", err))
	}

	return mapped
}

func badRequest(msg string) error {
	return &contract.Error{Code: contract.CodeBadRequest, Message: msg}
}

// requireID trims an id and refuses an empty one.
func requireID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", badRequest("id is required")
	}

	return id, nil
}
