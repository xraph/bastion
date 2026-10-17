package bastion

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xraph/bastion/observability"
)

func TestGatewayMiddleware_ReportsIPFiltered(t *testing.T) {
	var got []observability.RejectReason

	h := gatewayMiddleware(
		Config{IPFilter: IPFilterConfig{Enabled: true, DenyIPs: []string{"192.0.2.1"}}},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		func(_ *http.Request, reason observability.RejectReason) { got = append(got, reason) },
	)

	for _, addr := range []string{"192.0.2.1:1234", "198.51.100.7:1234"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = addr

		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	if len(got) != 1 || got[0] != observability.RejectIPFiltered {
		t.Errorf("reported %v, want one ip_filtered", got)
	}
}
