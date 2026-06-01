// Package graph persists and queries the network/application topology in a
// Neo4j database. It models interfaces, devices, applications, services,
// customers and the paths (ordered hops across interfaces) that connect
// services, supporting upserts, time-bounded validity and impact analysis.
package graph

import "time"

// Node labels and relationship types used by the topology graph. The Label*
// constants are Neo4j node labels and the Rel* constants are relationship
// types referenced when building or querying the graph.
const (
	LabelInterface = "Interface"
	LabelDevice    = "Device"
	RelHop         = "HOP"
	RelTakes       = "TAKES"
	RelDependsOn   = "DEPENDS_ON"
)

// Iface is an observed network interface. PHash is its stable, content-derived
// primary key (the Neo4j "phash" property) used to identify the interface
// across observations; MetricIfName is the interface name as it appears in
// metrics, which may differ in casing or form from IfName. ObservedAt records
// when the interface was last seen.
type Iface struct {
	PHash, Device, IfName, MetricIfName, IfDescr, IfAlias, Instance, Vendor string
	IfIndex                                                                 int
	ObservedAt                                                              time.Time
}

// Hop is a single step along an application path: one interface traversed in a
// given Direction. Seq is its position within the path (ordered ascending),
// and Provenance records how the hop was derived (for example "declared").
type Hop struct {
	Device       string  `json:"device"`
	IfName       string  `json:"ifName"`
	MetricIfName string  `json:"metricIfName"`
	Instance     string  `json:"instance"`
	Direction    string  `json:"direction"`
	IfIndex      int     `json:"ifIndex"`
	Seq          int     `json:"seq"`
	Provenance   string  `json:"provenance"`
	Confidence   float64 `json:"confidence"`
}

// ImpactRow describes one application affected by an interface, including the
// owning service, consuming customer (empty when none) and business
// metadata used to assess the blast radius of that interface.
type ImpactRow struct {
	App         string `json:"app"`
	Service     string `json:"service"`
	Customer    string `json:"customer"`
	Owner       string `json:"owner"`
	Criticality string `json:"criticality"`
}
