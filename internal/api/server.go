// Package api exposes the read-only HTTP endpoints for querying the
// network graph: listing applications, resolving an application's path,
// and reporting the impact (blast radius) of an interface. It translates
// HTTP requests into Repo queries and encodes the results as JSON.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/catalog"
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
	// ListAllInterfaces returns every known interface, used by lookupImpact to
	// build a catalog.Resolver that maps raw query params to canonical phashes.
	ListAllInterfaces(ctx context.Context) ([]graph.Iface, error)
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

// minAtUnix is the lower bound for the "at" query parameter (Unix epoch, 1970-01-01T00:00:00Z).
// Pre-epoch timestamps are meaningless for network-graph temporal queries.
const minAtUnix int64 = 0

// maxAtUnix is the upper bound for the "at" query parameter (2100-01-01T00:00:00Z).
// Legitimate point-in-time queries fall well within this window; values beyond
// it are almost certainly bugs or attacks rather than real timestamps.
const maxAtUnix int64 = 4102444800

// at resolves the optional "at" query param to a point-in-time. When the param
// is absent it returns the current time. When present but unparseable, or
// outside the accepted range [minAtUnix, maxAtUnix] (Unix epoch to 2100-01-01),
// it returns ok=false so the handler can reject the request with a 400 rather
// than silently falling back to now or passing absurd instants to the graph.
func at(r *http.Request) (t time.Time, ok bool) {
	if v := r.URL.Query().Get("at"); v != "" {
		sec, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		if sec < minAtUnix || sec > maxAtUnix {
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

// lookupImpact resolves (device, ifName) to a canonical catalog interface and
// returns its impact rows. Resolution uses catalog.Resolver so that natural
// names (e.g. "Te0/1/2") map to the same canonical phash as the stored node
// (e.g. "tengige0/1/2"). Caller must inspect the error type to distinguish
// *catalog.NoMatchError, *catalog.AmbiguousError, and hard repo errors.
//
// TODO(OPT-10): this rebuilds the Resolver from a full ListAllInterfaces scan on
// every request. Acceptable for v1 catalogs; cache the Resolver with a TTL in the
// long-lived API process before this is on a hot path.
func (s *Server) lookupImpact(ctx context.Context, device, ifName string, t time.Time) (rows []graph.ImpactRow, resolvedPHash string, err error) {
	ifaces, err := s.repo.ListAllInterfaces(ctx)
	if err != nil {
		return nil, "", err
	}
	ifc, rerr := catalog.NewResolver(ifaces).Resolve(device, ifName)
	if rerr != nil {
		return nil, "", rerr
	}
	resolvedPHash = ifc.PHash
	rows, err = s.repo.InterfaceImpact(ctx, resolvedPHash, t)
	return
}

// writeImpact writes the shared wrapped response body for both /impact and
// /interface-apps. When rows is empty or nil, impact is [] (never null) and a
// "no path known" note is added.
func writeImpact(w http.ResponseWriter, device, ifName string, rows []graph.ImpactRow) {
	if rows == nil {
		rows = []graph.ImpactRow{}
	}
	if len(rows) == 0 {
		writeJSON(w, map[string]any{"interface": device + "/" + ifName, "impact": rows, "note": "no path known"})
		return
	}
	writeJSON(w, map[string]any{"interface": device + "/" + ifName, "impact": rows})
}

// impact reports the blast radius of an interface given as device/ifName query
// params. The ifName may be in any recognized form (canonical, metric, alias,
// description); the catalog resolver maps it to a canonical phash server-side.
func (s *Server) impact(w http.ResponseWriter, r *http.Request) {
	device, ifName := r.URL.Query().Get("device"), r.URL.Query().Get("ifName")
	if strings.TrimSpace(device) == "" || strings.TrimSpace(ifName) == "" {
		http.Error(w, "device and ifName are required", http.StatusBadRequest)
		return
	}
	t, ok := at(r)
	if !ok {
		http.Error(w, "invalid at parameter", http.StatusBadRequest)
		return
	}
	rows, _, err := s.lookupImpact(r.Context(), device, ifName, t)
	if err != nil {
		var noMatch *catalog.NoMatchError
		var ambiguous *catalog.AmbiguousError
		switch {
		case errors.As(err, &noMatch):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":       "no interface matches",
				"device":      noMatch.Device,
				"ifName":      noMatch.Ref,
				"suggestions": noMatch.Suggestions,
			})
		case errors.As(err, &ambiguous):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":   "ambiguous interface reference",
				"device":  ambiguous.Device,
				"ifName":  ambiguous.Ref,
				"matches": ambiguous.Matches,
			})
		default:
			log.Printf("api: InterfaceImpact: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}
	writeImpact(w, device, ifName, rows)
}

// ifaceApps is an alias for impact: both routes call the same handler so their
// response bodies are byte-identical. The /interface-apps route is kept for
// backward compatibility with the Grafana plugin.
func (s *Server) ifaceApps(w http.ResponseWriter, r *http.Request) {
	s.impact(w, r)
}
