package servicenow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// makeApps builds a slice of appResp.Result entries for test handlers.
func makeAppJSON(names []string) []byte {
	type row struct {
		Name    string `json:"name"`
		SysID   string `json:"sys_id"`
		Service string `json:"u_app_service"`
	}
	rows := make([]row, len(names))
	for i, n := range names {
		rows[i] = row{Name: n, SysID: fmt.Sprintf("id-%d", i), Service: "svc"}
	}
	b, _ := json.Marshal(map[string]any{"result": rows})
	return b
}

// makeNames returns n unique names like "app-0", "app-1", ...
func makeNames(prefix string, n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("%s-%d", prefix, i)
	}
	return names
}

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

func TestApplicationsPagination(t *testing.T) {
	// Override page size to something small so the test isn't unwieldy.
	orig := snPageSize
	snPageSize = 3
	defer func() { snPageSize = orig }()
	pageSize := snPageSize // snapshot for the handler goroutine (avoid racing the deferred restore)

	// Page 0: full page (3 rows). Page 1: partial page (2 rows). Total = 5.
	page0 := makeAppJSON(makeNames("p0app", 3))
	page1 := makeAppJSON(makeNames("p1app", 2))

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		q := r.URL.Query()
		lim := q.Get("sysparm_limit")
		if lim != strconv.Itoa(pageSize) {
			http.Error(w, "bad sysparm_limit: "+lim, http.StatusBadRequest)
			return
		}
		off, _ := strconv.Atoi(q.Get("sysparm_offset"))
		if off == 0 {
			w.Write(page0)
		} else {
			w.Write(page1)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "user", "pass")
	apps, err := c.Applications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 5 {
		t.Fatalf("want 5 apps, got %d: %+v", len(apps), apps)
	}
	// A name from page 2 must be present.
	found := false
	for _, a := range apps {
		if strings.HasPrefix(a.Name, "p1app-") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("page-2 name not found in result: %+v", apps)
	}
	if hits != 2 {
		t.Fatalf("expected 2 HTTP requests, got %d", hits)
	}
}

func TestApplicationsPaginationParams(t *testing.T) {
	// Verify sysparm_limit and sysparm_offset are sent correctly on every page.
	orig := snPageSize
	snPageSize = 2
	defer func() { snPageSize = orig }()
	pageSize := snPageSize // snapshot for the handler goroutine

	type pageRecord struct{ limit, offset string }
	var records []pageRecord

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		records = append(records, pageRecord{q.Get("sysparm_limit"), q.Get("sysparm_offset")})
		off, _ := strconv.Atoi(q.Get("sysparm_offset"))
		if off == 0 {
			// full page
			w.Write(makeAppJSON(makeNames("a", pageSize)))
		} else {
			// partial — terminates
			w.Write(makeAppJSON(makeNames("b", 1)))
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "u", "p")
	if _, err := c.Applications(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(records))
	}
	if records[0].limit != "2" || records[0].offset != "0" {
		t.Errorf("page0 params wrong: %+v", records[0])
	}
	if records[1].limit != "2" || records[1].offset != "2" {
		t.Errorf("page1 params wrong: %+v", records[1])
	}
}

func TestApplicationsPageCapExceeded(t *testing.T) {
	orig := snPageSize
	origMax := snMaxPages
	snPageSize = 1
	snMaxPages = 3
	defer func() {
		snPageSize = orig
		snMaxPages = origMax
	}()

	// Always return a full page — should error after snMaxPages pages.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(makeAppJSON(makeNames("x", 1))) // always full
	}))
	defer srv.Close()

	c := New(srv.URL, "u", "p")
	_, err := c.Applications(context.Background())
	if err == nil {
		t.Fatal("expected page-cap error, got nil")
	}
	if !strings.Contains(err.Error(), "page") {
		t.Errorf("error should mention page cap, got: %v", err)
	}
}

func TestApplicationsNon2xx(t *testing.T) {
	orig := retryBase
	retryBase = 1 * time.Millisecond
	t.Cleanup(func() { retryBase = orig })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "user", "pass")
	apps, err := c.Applications(context.Background())
	if err == nil {
		t.Fatalf("expected error on 500, got nil (apps=%+v)", apps)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should contain status code, got: %v", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should contain response body text, got: %v", err)
	}
}

func TestApplicationsContextCancel(t *testing.T) {
	orig := snPageSize
	snPageSize = 2
	defer func() { snPageSize = orig }()
	pageSize := snPageSize // snapshot for the handler goroutine

	ctx, cancel := context.WithCancel(context.Background())

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		// Cancel after first page is served.
		if hits == 1 {
			cancel()
		}
		w.Write(makeAppJSON(makeNames("a", pageSize)))
	}))
	defer srv.Close()

	c := New(srv.URL, "u", "p")
	_, err := c.Applications(ctx)
	// After context cancel the second page request should fail.
	if err == nil {
		t.Fatal("expected context cancellation error, got nil")
	}
}
