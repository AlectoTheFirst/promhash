package main

import (
	"context"
	"encoding/json"
	"os"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/datasource"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"github.com/AlectoTheFirst/promhash-datasource/pkg/plugin"
)

func main() {
	if err := datasource.Manage("alectothefirst-promhash-datasource", newInstance, datasource.ManageOpts{}); err != nil {
		log.DefaultLogger.Error(err.Error())
		os.Exit(1)
	}
}

func newInstance(_ context.Context, s backend.DataSourceInstanceSettings) (instancemgmt.Instance, error) {
	var cfg struct {
		APIURL string `json:"apiUrl"`
	}
	_ = json.Unmarshal(s.JSONData, &cfg)
	return plugin.NewDatasource(cfg.APIURL), nil
}
