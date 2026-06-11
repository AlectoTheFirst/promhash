package alertenrich

import (
	"sync"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

// cacheTTL bounds how long a successful lookup is reusable. It must outlive a
// typical firing-to-resolved window so the resolved alert can reuse the firing
// enrichment when the API is unreachable; the rows themselves cannot go stale
// inside the window because the lookup is keyed by the alert's startsAt and
// the graph answers point-in-time queries from immutable history.
const cacheTTL = 6 * time.Hour

// cacheMaxEntries caps the cache size. One entry is a correlation key plus a
// handful of impact rows, so even the cap is a few MB at worst.
const cacheMaxEntries = 8192

// cacheKey identifies one alert instance's lookup: the correlation key plus
// the alert's startsAt. Prometheus sends identical startsAt values on the
// firing and resolved notifications of the same alert instance, so both map
// to the same entry.
type cacheKey struct {
	key    Key
	atUnix int64
}

// lookupCache is a bounded TTL cache of successful impact lookups. It exists
// for one guarantee: an alert whose firing notification was enriched must get
// the SAME derived labels on its resolved notification even if the lookup
// fails at resolve time — otherwise the fingerprints differ and the alert
// only clears via Alertmanager's resolve_timeout. It is best-effort by
// design: a cache miss degrades to plain fail-open passthrough.
type lookupCache struct {
	mu      sync.Mutex
	entries map[cacheKey]cacheEntry
	now     func() time.Time
}

type cacheEntry struct {
	rows    []graph.ImpactRow
	expires time.Time
}

func newLookupCache(now func() time.Time) *lookupCache {
	return &lookupCache{entries: map[cacheKey]cacheEntry{}, now: now}
}

// get returns the cached rows for (key, at) if present and unexpired.
func (c *lookupCache) get(key Key, at time.Time) ([]graph.ImpactRow, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[cacheKey{key: key, atUnix: at.Unix()}]
	if !ok || c.now().After(e.expires) {
		return nil, false
	}
	return e.rows, true
}

// put stores rows for (key, at). At the size cap it first drops expired
// entries, then arbitrary ones — the cache is best-effort, so losing an entry
// only costs the fingerprint guarantee for that one alert in the rare case
// the API is also down at its resolve time.
func (c *lookupCache) put(key Key, at time.Time, rows []graph.ImpactRow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= cacheMaxEntries {
		now := c.now()
		for k, e := range c.entries {
			if now.After(e.expires) {
				delete(c.entries, k)
			}
		}
		for k := range c.entries {
			if len(c.entries) < cacheMaxEntries {
				break
			}
			delete(c.entries, k)
		}
	}
	c.entries[cacheKey{key: key, atUnix: at.Unix()}] = cacheEntry{rows: rows, expires: c.now().Add(cacheTTL)}
}
