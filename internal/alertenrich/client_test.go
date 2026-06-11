package alertenrich

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAPIClientExact(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/impact" {
			t.Errorf("path %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("instance") != "10.0.0.1:161" || q.Get("ifIndex") != "42" {
			t.Errorf("query %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"interface":"10.0.0.1:161/42","impact":[{"app":"payments","service":"payments-api","owner":"team-payments"}]}`))
	}))
	defer srv.Close()

	c := NewAPIClient(srv.URL, srv.Client(), "")
	rows, err := c.ImpactByInstanceIndex(context.Background(), "10.0.0.1:161", 42, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].App != "payments" {
		t.Fatalf("rows %+v", rows)
	}
}

func TestAPIClientSendsBearerToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer s3cret" {
			t.Errorf("Authorization = %q, want Bearer s3cret", got)
		}
		_, _ = w.Write([]byte(`{"impact":[]}`))
	}))
	defer srv.Close()

	c := NewAPIClient(srv.URL, srv.Client(), "s3cret")
	if _, err := c.ImpactByInstanceIndex(context.Background(), "10.0.0.1", 1, time.Time{}); err != nil {
		t.Fatal(err)
	}
}

func TestAPIClientNotFoundIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no interface matches"}`))
	}))
	defer srv.Close()

	c := NewAPIClient(srv.URL, srv.Client(), "")
	rows, err := c.ImpactByName(context.Background(), "rtr-x", "Te9", time.Time{})
	if err != nil {
		t.Fatalf("404 must map to empty rows + nil error, got %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected empty rows, got %+v", rows)
	}
}

func TestAPIClientServerErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewAPIClient(srv.URL, srv.Client(), "")
	if _, err := c.ImpactByInstanceIndex(context.Background(), "10.0.0.1", 1, time.Time{}); err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestAPIClientAtParam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("at") != "1700000000" {
			t.Errorf("at=%q", r.URL.Query().Get("at"))
		}
		_, _ = w.Write([]byte(`{"impact":[]}`))
	}))
	defer srv.Close()

	c := NewAPIClient(srv.URL, srv.Client(), "")
	_, _ = c.ImpactByInstanceIndex(context.Background(), "10.0.0.1", 1, time.Unix(1700000000, 0))
}
