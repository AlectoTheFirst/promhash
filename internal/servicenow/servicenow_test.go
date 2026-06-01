package servicenow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestApplications(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"result":[{"name":"payments","sys_id":"abc","u_app_service":"payments-api"}]}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "user", "pass")
	apps, err := c.Applications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || apps[0].Name != "payments" || apps[0].Service != "payments-api" {
		t.Fatalf("got %+v", apps)
	}
}
