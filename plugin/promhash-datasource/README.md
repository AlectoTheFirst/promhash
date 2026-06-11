# promhash-datasource (Grafana plugin, C8)

A Grafana data source plugin that queries the promhash application-path graph via the
promhash HTTP API (C7). It has a Go backend (`pkg/`) and a React/TypeScript frontend (`src/`).

## Query types

- `app_path` — resolve the candidate hop paths for an application.
- `impact` — resolve the impacted apps/services for a device interface.

The config editor exposes two settings: **promhash API URL** (`jsonData.apiUrl`), which the
backend uses to reach the C7 API, and **API token** (`secureJsonData.apiToken`) — the Bearer
token presented on every upstream call. promhash-api requires a token unless it runs with
`-insecure-no-auth`; a wrong or missing token surfaces as an explicit 401 message in the
datasource health check.

## Build

### Backend

Grafana resolves the executable as `gpx_promhash_<GOOS>_<GOARCH>`, so build for the
platform your Grafana server runs on:

```bash
GOOS=linux GOARCH=amd64 go build -o dist/gpx_promhash_linux_amd64 ./pkg
# (or `mage` to build the full platform matrix via the plugin SDK)
```

### Frontend

```bash
npm install
npm run build      # webpack production build: dist/module.js + dist/plugin.json
npx tsc --noEmit   # type-check only (smoke)
```

`make dist-plugin` from the repo root runs both steps.

## Signing

The plugin id is `alectothefirst-promhash-datasource`. Grafana requires plugins to be signed.
Choose one:

- **Private signing (Grafana Enterprise):** sign the built `dist/` with a private signature
  scoped to the root URLs of the Grafana instances that will load it
  (`grafana-sign-plugin --rootUrls https://grafana.example.com`). Use this for production.
- **Unsigned (dev / internal only):** add the plugin id to the Grafana config so it loads
  unsigned:

  ```ini
  [plugins]
  allow_loading_unsigned_plugins = alectothefirst-promhash-datasource
  ```

  or via env: `GF_PLUGINS_ALLOW_LOADING_UNSIGNED_PLUGINS=alectothefirst-promhash-datasource`.

## Deploy

There is no release pipeline; deployment is copying the built `dist/` directory to the
Grafana server:

1. `make dist-plugin` (repo root) — builds `dist/module.js`, `dist/plugin.json`, and the
   linux backend binaries.
2. Copy `dist/` to `<grafana-plugins-dir>/alectothefirst-promhash-datasource/` (default
   plugins dir: `/var/lib/grafana/plugins`).
3. Sign it, or allow it unsigned (see Signing above).
4. Restart Grafana, add the data source, set the promhash API URL and the API token.
