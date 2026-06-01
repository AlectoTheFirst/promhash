//go:build mage

package main

// This is required to be able to run mage targets from the plugin SDK.
import (
	// mage:import
	build "github.com/grafana/grafana-plugin-sdk-go/build"
)

// Default configures the default target.
var Default = build.BuildAll
