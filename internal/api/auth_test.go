package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// authedServer wraps the standard fakeRepo server with two valid tokens.
func authedServer() http.Handler {
	return WithAuth(NewServer(fakeRepo{}).Mux(), []string{"token-one", "token-two"})
}

func get(t *testing.T, h http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	h.ServeHTTP(rec, req)
	return rec
}

func TestWithAuthRejectsMissingToken(t *testing.T) {
	rec := get(t, authedServer(), "/apps", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("401 must carry WWW-Authenticate")
	}
}

func TestWithAuthRejectsWrongToken(t *testing.T) {
	if rec := get(t, authedServer(), "/apps", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong token, got %d", rec.Code)
	}
}

func TestWithAuthAcceptsAnyConfiguredToken(t *testing.T) {
	for _, tok := range []string{"token-one", "token-two"} {
		if rec := get(t, authedServer(), "/apps", tok); rec.Code != http.StatusOK {
			t.Fatalf("token %q: expected 200, got %d body %s", tok, rec.Code, rec.Body)
		}
	}
}

func TestWithAuthSchemeIsCaseInsensitive(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/apps", nil)
	req.Header.Set("Authorization", "bearer token-one")
	authedServer().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("lowercase scheme: expected 200, got %d", rec.Code)
	}
}

func TestWithAuthExemptsOperationalEndpoints(t *testing.T) {
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		if rec := get(t, authedServer(), path, ""); rec.Code == http.StatusUnauthorized {
			t.Errorf("%s must be exempt from auth, got 401", path)
		}
	}
}

func TestWithAuthPanicsOnEmptyTokens(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for empty token set")
		}
	}()
	WithAuth(NewServer(fakeRepo{}).Mux(), nil)
}
