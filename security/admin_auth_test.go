package security

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminAuth_Disabled(t *testing.T) {
	m := NewAdminAuthMiddleware(AdminAuthConfig{Enabled: false})

	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/routes", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("disabled auth should pass through, got %d", rec.Code)
	}
}

func TestAdminAuth_MissingKey(t *testing.T) {
	m := NewAdminAuthMiddleware(AdminAuthConfig{
		Enabled: true,
		APIKeys: []string{"secret-key"},
	})

	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/routes", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing key, got %d", rec.Code)
	}
}

func TestAdminAuth_InvalidKey(t *testing.T) {
	m := NewAdminAuthMiddleware(AdminAuthConfig{
		Enabled: true,
		APIKeys: []string{"secret-key"},
	})

	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/routes", nil)
	req.Header.Set("X-Admin-Key", "wrong-key")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for invalid key, got %d", rec.Code)
	}
}

func TestAdminAuth_ValidKey(t *testing.T) {
	m := NewAdminAuthMiddleware(AdminAuthConfig{
		Enabled: true,
		APIKeys: []string{"secret-key"},
	})

	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/routes", nil)
	req.Header.Set("X-Admin-Key", "secret-key")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for valid key, got %d", rec.Code)
	}
}

func TestAdminAuth_CustomHeader(t *testing.T) {
	m := NewAdminAuthMiddleware(AdminAuthConfig{
		Enabled: true,
		APIKeys: []string{"my-key"},
		Header:  "Authorization",
	})

	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/routes", nil)
	req.Header.Set("Authorization", "my-key")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 with custom header, got %d", rec.Code)
	}
}

func TestAdminAuth_MultipleKeys(t *testing.T) {
	m := NewAdminAuthMiddleware(AdminAuthConfig{
		Enabled: true,
		APIKeys: []string{"key-1", "key-2", "key-3"},
	})

	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, key := range []string{"key-1", "key-2", "key-3"} {
		req := httptest.NewRequest(http.MethodGet, "/admin/routes", nil)
		req.Header.Set("X-Admin-Key", key)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200 for key %q, got %d", key, rec.Code)
		}
	}
}
