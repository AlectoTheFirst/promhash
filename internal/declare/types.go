package declare

import "gopkg.in/yaml.v3"

type Hop struct {
	Device    string `yaml:"device"`
	If        string `yaml:"if"`
	Direction string `yaml:"direction"`
}
type Path struct {
	Hops []Hop `yaml:"hops"`
}
type Dependency struct {
	To    string `yaml:"to"`
	Path  *Path  `yaml:"path"`  // sugar: single candidate
	Paths []Path `yaml:"paths"` // candidate set
}
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

func Parse(b []byte) (App, error) {
	var a App
	err := yaml.Unmarshal(b, &a)
	return a, err
}
