// Package alertenrich enriches Alertmanager v2 alerts in flight with graph
// blast-radius data. It parses the alert batch, correlates each alert to an
// interface, looks up impact, and stamps bounded labels + rich annotations,
// forwarding the result to the upstream Alertmanager. It fails open: any error
// leaves the alert unchanged.
package alertenrich

import "encoding/json"

// rawAlert holds every field of one Alertmanager v2 alert as raw JSON so that
// fields the proxy does not touch (startsAt, endsAt, generatorURL, and any
// future fields) round-trip byte-for-byte. Only "labels" and "annotations" are
// ever decoded and re-encoded.
type rawAlert map[string]json.RawMessage

// parseAlerts decodes the POST /api/v2/alerts body (a JSON array) into rawAlerts.
func parseAlerts(body []byte) ([]rawAlert, error) {
	var out []rawAlert
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// marshalAlerts re-encodes the batch. Fields untouched by the proxy are emitted
// from their stored raw JSON unchanged.
func marshalAlerts(as []rawAlert) ([]byte, error) { return json.Marshal(as) }

// decodeStringMap decodes the named object field into a map[string]string.
// A missing field yields a non-nil empty map so callers can always add keys.
func decodeStringMap(a rawAlert, field string) (map[string]string, error) {
	m := map[string]string{}
	raw, ok := a[field]
	if !ok || len(raw) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func labelsOf(a rawAlert) (map[string]string, error)      { return decodeStringMap(a, "labels") }
func annotationsOf(a rawAlert) (map[string]string, error) { return decodeStringMap(a, "annotations") }

// encodeStringMap re-encodes m into the named object field.
func encodeStringMap(a rawAlert, field string, m map[string]string) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	a[field] = raw
	return nil
}

func setLabels(a rawAlert, m map[string]string) error { return encodeStringMap(a, "labels", m) }
func setAnnotations(a rawAlert, m map[string]string) error {
	return encodeStringMap(a, "annotations", m)
}

// decodeString decodes the named string field; returns "" when absent.
func decodeString(a rawAlert, field string) string {
	raw, ok := a[field]
	if !ok || len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

func startsAtOf(a rawAlert) string { return decodeString(a, "startsAt") }
func endsAtOf(a rawAlert) string   { return decodeString(a, "endsAt") }
