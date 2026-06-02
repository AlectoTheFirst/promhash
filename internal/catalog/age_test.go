package catalog

import (
	"testing"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

func TestOldestObservedAt(t *testing.T) {
	t1 := time.Unix(1700000000, 0).UTC()
	t2 := time.Unix(1700086400, 0).UTC() // t1 + 24h
	t3 := time.Unix(1699913600, 0).UTC() // t1 - 24h (oldest)

	t.Run("mixed zero and non-zero returns minimum non-zero", func(t *testing.T) {
		ifaces := []graph.Iface{
			{ObservedAt: t1},
			{ObservedAt: time.Time{}}, // zero — should be ignored
			{ObservedAt: t2},
			{ObservedAt: t3},
			{ObservedAt: time.Time{}}, // zero — should be ignored
		}
		got := OldestObservedAt(ifaces)
		if !got.Equal(t3) {
			t.Fatalf("want %v, got %v", t3, got)
		}
	})

	t.Run("all zero returns zero time", func(t *testing.T) {
		ifaces := []graph.Iface{
			{ObservedAt: time.Time{}},
			{ObservedAt: time.Time{}},
		}
		got := OldestObservedAt(ifaces)
		if !got.IsZero() {
			t.Fatalf("want zero time, got %v", got)
		}
	})

	t.Run("empty slice returns zero time", func(t *testing.T) {
		got := OldestObservedAt(nil)
		if !got.IsZero() {
			t.Fatalf("want zero time, got %v", got)
		}
	})

	t.Run("single non-zero entry returns that entry", func(t *testing.T) {
		ifaces := []graph.Iface{{ObservedAt: t1}}
		got := OldestObservedAt(ifaces)
		if !got.Equal(t1) {
			t.Fatalf("want %v, got %v", t1, got)
		}
	})
}
