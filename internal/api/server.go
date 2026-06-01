// Package api exposes the read-only HTTP endpoints for querying the
// network graph: listing applications, resolving an application's path,
// and reporting the impact (blast radius) of an interface. It translates
// HTTP requests into Repo queries and encodes the results as JSON.
package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/graph"
	"github.com/AlectoTheFirst/promhash/internal/phash"
)

// Repo is the data source the Server queries to answer requests. It abstracts
// the underlying graph store so handlers depend only on the operations they
// need. All queries are evaluated as of the supplied at time, allowing
// point-in-time views of a graph that changes over time.
type Repo interface {
	// AppPath returns the ordered hops an application's traffic traverses,
	// identified by its phash, as of the given time.
	AppPath(ctx context.Context, appPHash string, at time.Time) ([]graph.Hop, error)
	// InterfaceImpact returns the rows describing what is affected by the
	// interface identified by its phash, as of the given time.
	InterfaceImpact(ctx context.Context, ifacePHash string, at time.Time) ([]graph.ImpactRow, error)
	// ListApps returns the identifiers of all known applications.
	ListApps(ctx context.Context) ([]string, error)
}

// Server routes the HTTP API endpoints to handlers backed by a Repo.
type Server struct {
	repo Repo
	mux  *http.ServeMux
}

// NewServer constructs a Server backed by r and registers all API routes on
// its multiplexer. Use Mux to obtain the handler for serving.
func NewServer(r Repo) *Server {
	s := &Server{repo: r, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /apps", s.listApps)
	s.mux.HandleFunc("GET /apps/{app}/path", s.appPath)
	s.mux.HandleFunc("GET /interface-apps", s.ifaceApps)
	s.mux.HandleFunc("GET /impact", s.impact)
	return s
}

// Mux returns the underlying request multiplexer with all API routes
// registered, suitable for passing to http.Server or wrapping in middleware.
func (s *Server) Mux() *http.ServeMux { return s.mux }

// at resolves the optional "at" query param to a point-in-time. When the param
// is absent it returns the current time. When present but unparseable it
// returns ok=false so the handler can reject the request with a 400 rather than
// silently falling back to now.
func at(r *http.Request) (t time.Time, ok bool) {
	if v := r.URL.Query().Get("at"); v != "" {
		sec, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(sec, 0).UTC(), true
	}
	return time.Now(), true
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) listApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.repo.ListApps(r.Context())
	if err != nil {
		log.Printf("api: ListApps: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, apps)
}
func (s *Server) appPath(w http.ResponseWriter, r *http.Request) {
	t, ok := at(r)
	if !ok {
		http.Error(w, "invalid at parameter", http.StatusBadRequest)
		return
	}
	hops, err := s.repo.AppPath(r.Context(), phash.Hash(phash.KindApp, r.PathValue("app")), t)
	if err != nil {
		log.Printf("api: AppPath: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if hops == nil {
		hops = []graph.Hop{} // explicit empty, never null
	}
	writeJSON(w, hops)
}

// ifaceApps reverse-resolves an interface to the apps that traverse it. The
// device and ifName query params must be the canonical form produced by C9;
// canonical ifNames contain '/', so they are passed as query params rather than
// path segments (a single path wildcard would not match them). It resolves the
// interface phash server-side, the same way impact does.
func (s *Server) ifaceApps(w http.ResponseWriter, r *http.Request) {
	t, ok := at(r)
	if !ok {
		http.Error(w, "invalid at parameter", http.StatusBadRequest)
		return
	}
	device, ifName := r.URL.Query().Get("device"), r.URL.Query().Get("ifName")
	p := phash.Hash(phash.KindIface, device, ifName)
	rows, err := s.repo.InterfaceImpact(r.Context(), p, t)
	if err != nil {
		log.Printf("api: InterfaceImpact: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, rows)
}

// impact reports the blast radius of an interface given as device/ifName query
// params, which must be the canonical form produced by C9.
func (s *Server) impact(w http.ResponseWriter, r *http.Request) {
	t, ok := at(r)
	if !ok {
		http.Error(w, "invalid at parameter", http.StatusBadRequest)
		return
	}
	device, ifName := r.URL.Query().Get("device"), r.URL.Query().Get("ifName")
	p := phash.Hash(phash.KindIface, device, ifName)
	rows, err := s.repo.InterfaceImpact(r.Context(), p, t)
	if err != nil {
		log.Printf("api: InterfaceImpact: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if len(rows) == 0 {
		writeJSON(w, map[string]any{"interface": device + "/" + ifName, "impact": []graph.ImpactRow{}, "note": "no path known"})
		return
	}
	writeJSON(w, map[string]any{"interface": device + "/" + ifName, "impact": rows})
}
