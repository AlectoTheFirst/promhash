package alertenrich

import "testing"

func defaultMap() LabelMap {
	return LabelMap{DeviceLabel: "instance", IfIndexLabel: "ifIndex", IfNameLabel: "ifName"}
}

func TestCorrelateExact(t *testing.T) {
	k, ok := Correlate(map[string]string{"instance": "10.0.0.1:161", "ifIndex": "42"}, defaultMap())
	if !ok || k.Kind != KeyExact || k.Instance != "10.0.0.1:161" || k.IfIndex != 42 {
		t.Fatalf("got %+v ok=%v", k, ok)
	}
}

func TestCorrelateNameFallback(t *testing.T) {
	k, ok := Correlate(map[string]string{"instance": "rtr-core-1", "ifName": "Te0/1/2"}, defaultMap())
	if !ok || k.Kind != KeyName || k.Device != "rtr-core-1" || k.IfName != "Te0/1/2" {
		t.Fatalf("got %+v ok=%v", k, ok)
	}
}

func TestCorrelateNoDevice(t *testing.T) {
	if _, ok := Correlate(map[string]string{"ifIndex": "42"}, defaultMap()); ok {
		t.Fatal("expected no key when device label absent")
	}
}

func TestCorrelateNoIfaceLabels(t *testing.T) {
	if _, ok := Correlate(map[string]string{"instance": "10.0.0.1"}, defaultMap()); ok {
		t.Fatal("expected no key when neither ifIndex nor ifName present")
	}
}

func TestCorrelateNonIntIfIndexFallsToName(t *testing.T) {
	k, ok := Correlate(map[string]string{"instance": "rtr-core-1", "ifIndex": "n/a", "ifName": "Te0/1/2"}, defaultMap())
	if !ok || k.Kind != KeyName {
		t.Fatalf("expected name fallback when ifIndex non-integer, got %+v ok=%v", k, ok)
	}
}

func TestCorrelateCustomLabels(t *testing.T) {
	m := LabelMap{DeviceLabel: "host", IfIndexLabel: "idx", IfNameLabel: "iface"}
	k, ok := Correlate(map[string]string{"host": "10.0.0.2", "idx": "7"}, m)
	if !ok || k.Kind != KeyExact || k.Instance != "10.0.0.2" || k.IfIndex != 7 {
		t.Fatalf("got %+v ok=%v", k, ok)
	}
}
