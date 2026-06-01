# promhash-datasource (Grafana plugin, C8)

A Grafana data source plugin that queries the promhash application-path graph via the
promhash HTTP API (C7). It has a Go backend (`pkg/`) and a React/TypeScript frontend (`src/`).

## Query types

- `app_path` — resolve the candidate hop paths for an application.
- `impact` — resolve the impacted apps/services for a device interface.

The config editor exposes a single setting, **promhash API URL** (`jsonData.apiUrl`), which
the backend uses to reach the C7 API.

## Build

### Backend

Build the backend executable into `dist/` (Grafana loads it as `gpx_promhash`):

```bash
# via mage (preferred)
mage

# or directly
go build -o dist/gpx_promhash ./pkg
```

### Frontend

```bash
npm install
npm run build      # production webpack build into dist/
npx tsc --noEmit   # type-check only (smoke)
```

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

Deploy the built `dist/` directory (backend executable + frontend bundle + `plugin.json` +
signature) via the existing GitOps pipeline that provisions Grafana plugins. Configure the
data source with the promhash API URL after the plugin is loaded.
