package graph

import "time"

const (
	LabelInterface = "Interface"
	LabelDevice    = "Device"
	RelHop         = "HOP"
	RelTakes       = "TAKES"
	RelDependsOn   = "DEPENDS_ON"
)

type Iface struct {
	PHash, Device, IfName, MetricIfName, IfDescr, IfAlias, Instance, Vendor string
	IfIndex                                                                 int
	ObservedAt                                                              time.Time
}

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

type ImpactRow struct {
	App         string `json:"app"`
	Service     string `json:"service"`
	Customer    string `json:"customer"`
	Owner       string `json:"owner"`
	Criticality string `json:"criticality"`
}
