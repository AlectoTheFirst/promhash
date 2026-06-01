package nautobot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeviceInstanceMap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"results":[{"name":"rtr-core-1","primary_ip":{"address":"10.0.0.1/32"}}]}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "token")
	m, err := c.DeviceInstanceMap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m["rtr-core-1"] != "10.0.0.1" {
		t.Fatalf("got %v", m)
	}
}

func TestDeviceInstanceMapNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"detail":"boom"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "token")
	m, err := c.DeviceInstanceMap(context.Background())
	if err == nil {
		t.Fatalf("expected error on 500, got nil (m=%v)", m)
	}
}
