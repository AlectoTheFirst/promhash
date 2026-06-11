#!/bin/sh
# One-shot demo bootstrap: catalog sync -> load declarations -> generate the
# _shared/ projection artifacts into the shared /artifacts volume.
set -eu

NEO="bolt://neo4j:7687"

echo "init: waiting for demo series in main-prom..."
i=0
until wget -qO- 'http://main-prom:9090/api/v1/query?query=ifHCInOctets' 2>/dev/null | grep -q '"hostname"'; do
  i=$((i + 1))
  if [ "$i" -gt 60 ]; then
    echo "init: timed out waiting for ifHCInOctets series" >&2
    exit 1
  fi
  sleep 2
done

echo "init: catalog sync (device names from the hostname label)..."
i=0
until promhash-catalog -neo4j "$NEO" -prometheus http://main-prom:9090 -device-label hostname; do
  i=$((i + 1))
  if [ "$i" -gt 30 ]; then
    echo "init: catalog sync failed" >&2
    exit 1
  fi
  sleep 2
done

echo "init: validating + loading declarations..."
promhash-loader -dir /demo/declared -neo4j "$NEO" -validate-only
promhash-loader -dir /demo/declared -neo4j "$NEO" -source demo

echo "init: generating projection artifacts..."
promhash-enrich -neo4j "$NEO" -apps payments,checkout -out /artifacts \
  -mapping-target mapping:80 \
  -remote-write-url http://lts-prom:9090/api/v1/write \
  -tenant-label demo

echo "init: done — artifacts in /artifacts/_shared"
ls -l /artifacts/_shared
