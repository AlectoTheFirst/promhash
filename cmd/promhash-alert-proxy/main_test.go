package main

import (
	"reflect"
	"testing"
	"time"
)

func TestParseUpstreams(t *testing.T) {
	got := parseUpstreams("http://am-0:9093, http://am-1:9093 ,")
	want := []string{"http://am-0:9093", "http://am-1:9093"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseUpstreamsEmpty(t *testing.T) {
	if got := parseUpstreams("   "); len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestBuildConfigDefaults(t *testing.T) {
	c := buildConfig(opts{
		upstreams: "http://am:9093",
		apiBase:   "http://api:8080",
		timeout:   3 * time.Second,
	})
	if c.LabelMap.DeviceLabel != "instance" || c.LabelMap.IfIndexLabel != "ifIndex" || c.LabelMap.IfNameLabel != "ifName" {
		t.Fatalf("label map defaults wrong: %+v", c.LabelMap)
	}
	if len(c.Upstreams) != 1 || c.Upstreams[0] != "http://am:9093" {
		t.Fatalf("upstreams: %+v", c.Upstreams)
	}
	if c.Timeout != 3*time.Second {
		t.Fatalf("timeout: %v", c.Timeout)
	}
}
