// Package declare parses application dependency declarations from YAML and
// loads them into the graph. A declaration describes an app, the service it
// runs as, and the network paths through which it depends on other services;
// declarations are validated against the device/interface catalog before being
// resolved into persisted graph edges.
package declare

import "gopkg.in/yaml.v3"

// Hop is a single device/interface step along a declared network path.
type Hop struct {
	Device string `yaml:"device"`
	If     string `yaml:"if"`
	// Direction is the traffic direction at this hop relative to the device
	// (e.g. "egress"), used downstream to orient enrichment and matching.
	Direction string `yaml:"direction"`
}

// Path is an ordered sequence of hops describing one route a dependency may take.
type Path struct {
	Hops []Hop `yaml:"hops"`
}

// Dependency declares that an app depends on another service (To), reachable via
// one or more candidate network paths. Use Candidates to read the path set,
// which normalizes the Path/Paths sugar.
type Dependency struct {
	To    string `yaml:"to"`
	Path  *Path  `yaml:"path"`  // sugar: single candidate
	Paths []Path `yaml:"paths"` // candidate set
}

// App is a parsed declaration: the application, the service it runs as, its
// owner, the customers that consume it, and the services it depends on.
type App struct {
	App                 string       `yaml:"app"`
	RunsAs              string       `yaml:"runs_as"`
	Owner               string       `yaml:"owner"`
	ConsumedByCustomers []string     `yaml:"consumed_by_customers"`
	DependsOn           []Dependency `yaml:"depends_on"`
}

// Candidates normalizes the `path`/`paths` sugar into a single slice.
func (d Dependency) Candidates() []Path {
	if d.Path != nil {
		return append([]Path{*d.Path}, d.Paths...)
	}
	return d.Paths
}

// Parse unmarshals the YAML bytes of a single declaration into an App.
func Parse(b []byte) (App, error) {
	var a App
	err := yaml.Unmarshal(b, &a)
	return a, err
}
