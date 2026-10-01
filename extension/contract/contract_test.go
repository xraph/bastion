package contract

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	dashcontract "github.com/xraph/forge/extensions/dashboard/contract"
	"github.com/xraph/forge/extensions/dashboard/contract/dispatcher"
)

func TestRegister_RequiresGatewayAndAdmin(t *testing.T) {
	d := dispatcher.New(nil)
	if err := Register(d, dashcontract.NewRegistry(), dashcontract.NewWardenRegistry(), Deps{}); err == nil {
		t.Error("Register with no deps returned no error")
	}
}

func TestEveryDeclaredIntentIsRegistered(t *testing.T) {
	deps, _, _, _ := newTestDeps(t)
	d := dispatcher.New(nil)
	if err := Register(d, dashcontract.NewRegistry(), dashcontract.NewWardenRegistry(), deps); err != nil {
		t.Fatalf("Register: %v", err)
	}

	for _, intent := range loadManifest(t).Intents {
		kind := dashcontract.KindQuery
		if intent.Kind == dashcontract.IntentKindCommand {
			kind = dashcontract.KindCommand
		}
		req := dashcontract.Request{
			Envelope: "v1", Kind: kind, Contributor: ContributorName, Intent: intent.Name, IntentVersion: 1,
		}
		if kind == dashcontract.KindQuery {
			req.Params = map[string]any{}
		} else {
			req.Payload = json.RawMessage(`{}`)
		}
		_, _, err := d.Dispatch(context.Background(), req, dashcontract.Principal{})
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "not registered") {
			t.Errorf("%s is not registered: %v", intent.Name, err)
		}
	}
}
