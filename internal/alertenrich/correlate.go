package alertenrich

import "strconv"

// LabelMap names the alert labels that carry the device/interface identity.
// Defaults: DeviceLabel "instance", IfIndexLabel "ifIndex", IfNameLabel "ifName".
type LabelMap struct {
	DeviceLabel  string
	IfIndexLabel string
	IfNameLabel  string
}

// KeyKind distinguishes the two correlation strategies.
type KeyKind int

const (
	KeyNone  KeyKind = iota // no usable correlation labels present
	KeyExact                // match Interface by (instance, ifIndex)
	KeyName                 // match Interface by (device-name, ifName) via the catalog resolver
)

// Key is a resolved correlation target for one alert.
type Key struct {
	Kind     KeyKind
	Instance string // KeyExact: value of the device label (Prometheus instance)
	IfIndex  int    // KeyExact
	Device   string // KeyName: value of the device label (device name)
	IfName   string // KeyName
}

// Correlate derives a correlation Key from an alert's labels using m. The device
// label is required. When it and an integer ifIndex label are present, an exact
// key is returned; otherwise, when a name label is present, a name key is
// returned. Returns ok=false when no usable key can be built (caller forwards
// the alert un-enriched).
func Correlate(labels map[string]string, m LabelMap) (Key, bool) {
	dev := labels[m.DeviceLabel]
	if dev == "" {
		return Key{}, false
	}
	if s := labels[m.IfIndexLabel]; s != "" {
		if idx, err := strconv.Atoi(s); err == nil {
			return Key{Kind: KeyExact, Instance: dev, IfIndex: idx}, true
		}
	}
	if name := labels[m.IfNameLabel]; name != "" {
		return Key{Kind: KeyName, Device: dev, IfName: name}, true
	}
	return Key{}, false
}
