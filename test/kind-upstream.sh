#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
cluster="${KIND_CLUSTER:-k8os-upstream-test-$$}"
created=false
applied=false
namespace="k8os-upstream-test"

cleanup() {
  if [[ "$applied" == true ]]; then
    KUBECONFIG="$work/kubeconfig" "$work/k8os" scenario cleanup upstream-dependency-v1 --namespace "$namespace" >/dev/null 2>&1 || true
  fi
  if [[ "$created" == true ]]; then kind delete cluster --name "$cluster" >/dev/null; fi
  rm -rf "$work"
}
trap cleanup EXIT

if [[ -z "${KIND_CLUSTER:-}" ]]; then
  created=true
  kind create cluster --name "$cluster" --wait 120s
fi
kind get kubeconfig --name "$cluster" > "$work/kubeconfig"
docker build -q -f "$repo_root/incident-fixture.Dockerfile" -t k8os-incident:test "$repo_root" >/dev/null
while IFS= read -r node; do
  docker save k8os-incident:test | docker exec -i "$node" ctr -n k8s.io images import - >/dev/null
done < <(kind get nodes --name "$cluster")
(cd "$repo_root" && go build -o "$work/k8os" ./cmd/k8os)

if KUBECONFIG="$work/kubeconfig" kubectl get namespace "$namespace" >/dev/null 2>&1; then
  echo "namespace $namespace already exists" >&2
  exit 1
fi
applied=true
KUBECONFIG="$work/kubeconfig" "$work/k8os" scenario apply upstream-dependency-v1 --namespace "$namespace" --image k8os-incident:test >/dev/null
KUBECONFIG="$work/kubeconfig" "$work/k8os" scenario trigger upstream-dependency-v1 --namespace "$namespace" --variant bridge > "$work/bridge.json"
KUBECONFIG="$work/kubeconfig" "$work/k8os" scenario trigger upstream-dependency-v1 --namespace "$namespace" --variant no-bridge > "$work/no-bridge.json"
KUBECONFIG="$work/kubeconfig" "$work/k8os" scenario evidence upstream-dependency-v1 --namespace "$namespace" > "$work/evidence.json"

python3 - "$work/bridge.json" "$work/no-bridge.json" "$work/evidence.json" "$namespace" <<'PY'
import hashlib
import json
import sys
from datetime import datetime

bridge, mirror, pods = [json.load(open(path, encoding="utf-8")) for path in sys.argv[1:4]]
namespace = sys.argv[4]
records = {}
locators = {}
upstream_uid = next(p["uid"] for p in pods if p["component"] == "upstream")
upstream_name = next(p["name"] for p in pods if p["component"] == "upstream")
for pod in pods:
    assert pod["sampled"] is True
    assert pod["image_id"]
    datetime.fromisoformat(pod["acquired_at"].replace("Z", "+00:00"))
    for entry in pod["lines"]:
        datetime.fromisoformat(entry["at"].replace("Z", "+00:00"))
        assert hashlib.sha256(entry["line"].encode()).hexdigest() == entry["sha256"]
        record = json.loads(entry["line"])
        if record["schema"] == "northstar.dependency-locator.v1":
            assert pod["component"] == "caller"
            assert record["kind"] == "Pod"
            assert record["namespace"] == namespace
            assert record["name"] == upstream_name
            assert record["uid"] == upstream_uid
            assert record["relation"] == "runtime_dependency"
            assert record["attempt"] not in locators
            locators[record["attempt"]] = record
            continue
        assert record["schema"] == "northstar.request.v1"
        assert record["binding_version"] == "v1"
        assert record["upstream_uid"] == upstream_uid
        records.setdefault(record["attempt"], {})[record["stage"]] = record

assert set(records[bridge["attempt"]]) == {"receiver_accepted", "receiver_responded", "caller_returned"}
assert set(records[mirror["attempt"]]) == {"caller_returned"}
assert set(locators) == {bridge["attempt"], mirror["attempt"]}
assert set(bridge["expected_stages"]) == set(records[bridge["attempt"]])
assert set(mirror["expected_stages"]) == set(records[mirror["attempt"]])
accepted = records[bridge["attempt"]]["receiver_accepted"]
responded = records[bridge["attempt"]]["receiver_responded"]
returned = records[bridge["attempt"]]["caller_returned"]
assert accepted["request_sha256"] == responded["request_sha256"] == returned["request_sha256"]
assert responded["response_sha256"] == returned["response_sha256"]
assert responded["status"] == returned["status"] == 503
print("upstream-dependency-v1: bridge, no-bridge, Pod UID, timestamps and digests passed")
PY

KUBECONFIG="$work/kubeconfig" "$work/k8os" scenario recover upstream-dependency-v1 --namespace "$namespace" >/dev/null
KUBECONFIG="$work/kubeconfig" "$work/k8os" scenario trigger upstream-dependency-v1 --namespace "$namespace" --variant recovered > "$work/recovered.json"
