#!/bin/sh
# One-shot demo bootstrap: catalog sync -> load declarations -> generate the
# _shared/ projection artifacts into the shared /artifacts volume.
set -eu

NEO="bolt://neo4j:7687"

# Wait for ALL 5 demo interfaces (2+2+1 across the three devices), not just
# the first series — the devices' first scrapes are staggered, and a partial
# catalog makes declaration validation fail.
echo "init: waiting for all demo series in main-prom..."
i=0
until wget -qO- 'http://main-prom:9090/api/v1/query?query=count(ifHCInOctets)' 2>/dev/null | grep -q ',"5"\]'; do
  i=$((i + 1))
  if [ "$i" -gt 60 ]; then
    echo "init: timed out waiting for the full ifHCInOctets series set" >&2
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
# The mapping series is served live by promhash-api; the evaluator scrape
# job reads its token from a file on the shared volume (never from the
# generated config itself).
printf 'demo-token' > /artifacts/api-token
promhash-enrich -neo4j "$NEO" -apps payments,checkout -out /artifacts \
  -promhash-api api:8080 \
  -api-token-file /artifacts/api-token \
  -remote-write-url http://lts-prom:9090/api/v1/write \
  -tenant-label demo

echo "init: done — artifacts in /artifacts/_shared"
ls -l /artifacts/_shared
