#!/bin/bash

# Runs a semi-realistic integration test with one producer generating metrics,
# a Observatorium API, a thanos receive for ingestion, a thanos query for querying the metrics
# and a loki for reading and writing logs.

set -euo pipefail

result=1
trap 'kill $(jobs -p); exit $result' EXIT

OBSERVATORIUM=${OBSERVATORIUM:=./observatorium}
DEX=${DEX:-dex}
PROMETHEUS=${PROMETHEUS:-prometheus}
THANOS=${THANOS:-thanos}
LOKI=${LOKI:-loki}
OPA=${OPA:-opa}
UP=${UP:-up}
WEBSOCAT=${WEBSOCAT:=websocat}

($DEX serve ./test/config/dex.yaml) &

echo "-------------------------------------------"
echo "- Getting authentication token...         -"
echo "-------------------------------------------"
sleep 2

token=$(curl --request POST \
  --silent \
  --cacert ./tmp/certs/ca.pem \
  --url https://localhost:5556/dex/token \
  --header 'content-type: application/x-www-form-urlencoded' \
  --data grant_type=password \
  --data username=admin@example.com \
  --data password=password \
  --data client_id=test \
  --data client_secret=ZXhhbXBsZS1hcHAtc2VjcmV0 \
  --data scope="openid email" | sed 's/^{.*"id_token":[^"]*"\([^"]*\)".*}/\1/')

(
  $OBSERVATORIUM \
    --web.listen=0.0.0.0:8443 \
    --web.internal.listen=0.0.0.0:8448 \
    --web.healthchecks.url=https://localhost:8443 \
    --tls.server.cert-file=./tmp/certs/server.pem \
    --tls.server.key-file=./tmp/certs/server.key \
    --tls.healthchecks.server-ca-file=./tmp/certs/ca.pem \
    --logs.read.endpoint=http://127.0.0.1:3100 \
    --logs.tail.endpoint=http://127.0.0.1:3100 \
    --logs.write.endpoint=http://127.0.0.1:3100 \
    --metrics.read.endpoint=http://127.0.0.1:9091 \
    --metrics.write.endpoint=http://127.0.0.1:19291 \
    --rbac.config=./test/config/rbac.yaml \
    --tenants.config=./test/config/tenants.yaml \
    --log.level=debug
) &

(
  $THANOS receive \
    --receive.hashrings-file=./test/config/hashrings.json \
    --receive.local-endpoint=127.0.0.1:10901 \
    --receive.default-tenant-id="1610b0c3-c509-4592-a256-a1871353dbfa" \
    --grpc-address=127.0.0.1:10901 \
    --http-address=127.0.0.1:10902 \
    --remote-write.address=127.0.0.1:19291 \
    --log.level=error \
    --tsdb.path="$(mktemp -d)"
) &

(
  $THANOS query \
    --grpc-address=127.0.0.1:10911 \
    --http-address=127.0.0.1:9091 \
    --store=127.0.0.1:10901 \
    --log.level=error \
    --web.external-prefix=/ui/metrics/v1
) &

(
  $LOKI \
    -log.level=error \
    -target=all \
    -config.file=./test/config/loki.yml
) &

echo "-------------------------------------------"
echo "- Waiting for dependencies to come up...  -"
echo "-------------------------------------------"
sleep 10

until curl --output /dev/null --silent --fail http://localhost:8448/ready; do
  printf '.'
  sleep 1
done

echo "-------------------------------------------"
echo "- Metrics tests                           -"
echo "-------------------------------------------"

if $UP \
  --listen=0.0.0.0:8888 \
  --endpoint-type=metrics \
  --tls-ca-file=./tmp/certs/ca.pem \
  --endpoint-read=https://127.0.0.1:8443/api/metrics/v1/test-oidc/api/v1/query \
  --endpoint-write=https://127.0.0.1:8443/api/metrics/v1/test-oidc/api/v1/receive \
  --period=500ms \
  --initial-query-delay=250ms \
  --threshold=1 \
  --latency=10s \
  --duration=10s \
  --log.level=error \
  --name=observatorium_write \
  --labels='_id="test"' \
  --token="$token"; then
  result=0
  echo "-------------------------------------------"
  echo "- Metrics tests: OK                        -"
  echo "-------------------------------------------"
else
  result=1
  echo "-------------------------------------------"
  echo "- Metrics tests: FAILED                   -"
  echo "-------------------------------------------"
  exit 1
fi

echo "-------------------------------------------"
echo "- Logs Read/Write tests                   -"
echo "-------------------------------------------"

if $UP \
  --listen=0.0.0.0:8888 \
  --endpoint-type=logs \
  --tls-ca-file=./tmp/certs/ca.pem \
  --tls-client-cert-file=./tmp/certs/client.pem \
  --tls-client-private-key-file=./tmp/certs/client.key \
  --endpoint-read=https://127.0.0.1:8443/api/logs/v1/test-mtls/api/v1/query \
  --endpoint-write=https://127.0.0.1:8443/api/logs/v1/test-mtls/api/v1/push \
  --period=500ms \
  --initial-query-delay=250ms \
  --threshold=1 \
  --latency=10s \
  --duration=10s \
  --log.level=error \
  --name=up_test \
  --labels='foo="bar"' \
  --logs="[\"$(date '+%s%N')\",\"log line 1\"]" \
  --token="$token"; then
  result=0
  echo "-------------------------------------------"
  echo "- Logs Read/Write tests: OK               -"
  echo "-------------------------------------------"
else
  result=1
  echo "-------------------------------------------"
  echo "- Logs Read/Write tests: FAILED           -"
  echo "-------------------------------------------"
  exit 1
fi

echo "-------------------------------------------"
echo "- Logs Tail/Write tests                   -"
echo "-------------------------------------------"

write_logs=$(curl \
               -v -H "Authorization: Bearer $token" -H "Content-Type: application/json" \
               --cacert ./tmp/certs/ca.pem \
               -XPOST -s https://127.0.0.1:8443/api/logs/v1/test-oidc/api/v1/push --data-raw \
               "{\"streams\": [{ \"stream\": { \"__name__\": \"up_test\", \"foo\": \"bar\" }, \"values\": [ [ \"$(date '+%s%N')\", \"log line 1\" ] ] }]}" \
               2> /dev/null && echo $?)

tail_logs=$($WEBSOCAT \
              -b -1 -U -H="Authorization: Bearer $token" \
              --ws-c-uri=wss://127.0.0.1:8443/api/logs/v1/test-oidc/api/v1/tail\?query=%7Bfoo%3D%22bar%22%2C__name__%3D%22up_test%22%7D  - \
              ws-c:cmd:'openssl s_client -connect 127.0.0.1:8443 -CAfile ./tmp/certs/ca.pem -quiet' \
              1> /dev/null && echo $?)

if [ "$write_logs" = "0" ] && [ "$tail_logs" = "0" ]; then
  result=0
  echo "-------------------------------------------"
  echo "- Logs Tail/Write tests: OK               -"
  echo "-------------------------------------------"
else
  result=1
  echo "-------------------------------------------"
  echo "- Logs Tail/Write tests: FAILED           -"
  echo "-------------------------------------------"
  exit 1
fi

echo "-------------------------------------------"
echo "- All tests: OK                           -"
echo "-------------------------------------------"
exit 0
