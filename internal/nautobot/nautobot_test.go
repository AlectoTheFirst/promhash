package nautobot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeviceInstanceMap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"results":[{"name":"rtr-core-1","primary_ip":{"address":"10.0.0.1/32"}}],"next":null,"count":1}`))
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

func TestDeviceInstanceMapPagination(t *testing.T) {
	var hits int
	var srvURL string

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		// Verify Authorization header is present on every request.
		if r.Header.Get("Authorization") != "Token testtoken" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		if hits == 1 {
			next := srvURL + "/api/dcim/devices/?limit=1000&offset=1000"
			fmt.Fprintf(w, `{"count":4,"next":%q,"results":[{"name":"dev-0","primary_ip":{"address":"10.0.0.0/32"}},{"name":"dev-1","primary_ip":{"address":"10.0.0.1/32"}}]}`, next)
		} else {
			w.Write([]byte(`{"count":4,"next":null,"results":[{"name":"dev-2","primary_ip":{"address":"10.0.0.2/32"}},{"name":"dev-3","primary_ip":{"address":"10.0.0.3/32"}}]}`))
		}
	})
	srv := httptest.NewServer(handler)
	srvURL = srv.URL
	defer srv.Close()

	c := New(srvURL, "testtoken")
	m, err := c.DeviceInstanceMap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 4 {
		t.Fatalf("want 4 devices, got %d: %v", len(m), m)
	}
	for _, name := range []string{"dev-0", "dev-1", "dev-2", "dev-3"} {
		if _, ok := m[name]; !ok {
			t.Errorf("device %q missing from result", name)
		}
	}
	if hits != 2 {
		t.Fatalf("expected 2 HTTP requests, got %d", hits)
	}
}

func TestDeviceInstanceMapPageCap(t *testing.T) {
	origMax := nbMaxPages
	nbMaxPages = 3
	defer func() { nbMaxPages = origMax }()

	var hits int
	var srvURL string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		next := fmt.Sprintf("%s/api/dcim/devices/?limit=1000&offset=%d", srvURL, hits*1000)
		fmt.Fprintf(w, `{"count":9999,"next":%q,"results":[{"name":"dev-%d","primary_ip":{"address":"10.0.0.1/32"}}]}`, next, hits)
	})
	srv := httptest.NewServer(handler)
	srvURL = srv.URL
	defer srv.Close()

	c := New(srvURL, "tok")
	_, err := c.DeviceInstanceMap(context.Background())
	if err == nil {
		t.Fatal("expected page-cap error, got nil")
	}
	if !strings.Contains(err.Error(), "page") {
		t.Errorf("error should mention page cap, got: %v", err)
	}
}

func TestDeviceInstanceMapHostMismatch(t *testing.T) {
	// A separate server that represents an attacker-controlled host.
	var attackerHits int
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerHits++
		w.Write([]byte(`{"count":0,"next":null,"results":[]}`))
	}))
	defer attacker.Close()

	var srvURL string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// First (and only) page returns next pointing at the attacker server.
		fmt.Fprintf(w, `{"count":1,"next":%q,"results":[{"name":"d0","primary_ip":{"address":"1.2.3.4/32"}}]}`, attacker.URL+"/api/dcim/devices/?limit=1000")
	})
	srv := httptest.NewServer(handler)
	srvURL = srv.URL
	defer srv.Close()

	c := New(srvURL, "secrettoken")
	_, err := c.DeviceInstanceMap(context.Background())
	if err == nil {
		t.Fatal("expected host-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("error should mention host mismatch, got: %v", err)
	}
	if attackerHits != 0 {
		t.Errorf("attacker server received %d requests; token may have been leaked", attackerHits)
	}
}

func TestDeviceInstanceMapNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"detail":"nope"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "token")
	m, err := c.DeviceInstanceMap(context.Background())
	if err == nil {
		t.Fatalf("expected error on 500, got nil (m=%v)", m)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should contain status code, got: %v", err)
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error should contain response body text, got: %v", err)
	}
}

func TestDeviceInstanceMapNoToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "unexpected auth header", http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"count":0,"next":null,"results":[]}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "") // empty token
	m, err := c.DeviceInstanceMap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 0 {
		t.Fatalf("expected empty map, got %v", m)
	}
}
