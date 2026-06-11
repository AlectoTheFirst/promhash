package alertenrich

import (
	"strings"
	"testing"
)

const sampleBatch = `[
  {"labels":{"alertname":"IfDown","instance":"10.0.0.1:161","ifIndex":"42"},
   "annotations":{"summary":"iface down"},
   "startsAt":"2026-06-03T10:00:00Z","endsAt":"2026-06-03T10:05:00Z",
   "generatorURL":"http://prom/graph"}
]`

func TestParseAndMarshalRoundTrip(t *testing.T) {
	alerts, err := parseAlerts([]byte(sampleBatch))
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 {
		t.Fatalf("want 1 alert, got %d", len(alerts))
	}
	lbls, err := labelsOf(alerts[0])
	if err != nil {
		t.Fatal(err)
	}
	if lbls["instance"] != "10.0.0.1:161" || lbls["ifIndex"] != "42" {
		t.Fatalf("labels: %+v", lbls)
	}
	if got := startsAtOf(alerts[0]); got != "2026-06-03T10:00:00Z" {
		t.Fatalf("startsAt: %q", got)
	}

	out, err := marshalAlerts(alerts)
	if err != nil {
		t.Fatal(err)
	}
	// Untouched fields must survive verbatim.
	for _, want := range []string{`"generatorURL":"http://prom/graph"`, `"endsAt":"2026-06-03T10:05:00Z"`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("round-trip dropped %s; got %s", want, out)
		}
	}
}

func TestSetLabelsAndAnnotations(t *testing.T) {
	alerts, _ := parseAlerts([]byte(sampleBatch))
	if err := setLabels(alerts[0], map[string]string{"alertname": "IfDown", "promhash_app_count": "3"}); err != nil {
		t.Fatal(err)
	}
	if err := setAnnotations(alerts[0], map[string]string{"summary": "iface down", "promhash_blast_radius": "3 apps"}); err != nil {
		t.Fatal(err)
	}
	out, _ := marshalAlerts(alerts)
	for _, want := range []string{`"promhash_app_count":"3"`, `"promhash_blast_radius":"3 apps"`, `"generatorURL":"http://prom/graph"`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
}

func TestEndsAtResolved(t *testing.T) {
	alerts, _ := parseAlerts([]byte(sampleBatch))
	if got := endsAtOf(alerts[0]); got != "2026-06-03T10:05:00Z" {
		t.Fatalf("endsAt: %q", got)
	}
}
