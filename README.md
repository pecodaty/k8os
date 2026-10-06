# k8os

k8os creates deliberately broken Kubernetes resources for testing how a cluster and its monitoring tools respond. It has a resource catalog (`inject`, `heal`, `cleanup`) and one versioned incident scenario (`upstream-dependency-v1`). Run it only against a cluster where you intend to create test workloads.

Build the CLI with Go 1.24.4 or later:

```bash
go build -o ./k8os ./cmd/k8os
```

The CLI uses in-cluster credentials when available and otherwise reads the default kubeconfig. Scenario commands also honor `KUBECONFIG` explicitly. Your Kubernetes account needs permission to create and manage the resources described below.

## Resource catalog

`inject` creates a namespace if needed, then creates a fixed set of broken Pods, workload controllers, Services, ConfigMaps, Secrets, PVCs, Ingresses, ServiceAccounts, Roles, and RoleBindings. Each kind is created 2 times in `light` mode, 5 times in `moderate`, or 10 times in `hell`. The default namespace is `k8os`. Existing objects are not updated or deleted by `inject`; a name collision causes the command to fail.

```bash
./k8os inject
./k8os inject --mode moderate --k8os-namespace chaos-lab
./k8os inject --mode hell --label team=platform --label env=dev
./k8os heal --k8os-namespace chaos-lab
./k8os cleanup --k8os-namespace chaos-lab
```

Generated resources carry `app.kubernetes.io/name=k8os`, `app.kubernetes.io/managed-by=k8os`, `k8os.io/mode`, and any labels supplied with `--label key=value`. `cleanup` deletes resources with both ownership labels from the selected namespace and leaves the namespace in place. `heal` attempts to give owned resources minimal healthy specs and adds `k8os.io/healed=true`. Kubernetes does not allow some specs to change in place, so `heal` deletes and recreates those resources under the same names. It does not heal every catalog kind.

The `chaos` package also has named fault fixtures for image changes, ConfigMap changes, Pod disruption budgets, unschedulable Pods, and a webhook with no endpoints. These fixtures are available through the Go package; the CLI has no command for them.

## Upstream dependency scenario

`upstream-dependency-v1` creates a caller and an upstream application in a new, dedicated namespace. The upstream initially returns HTTP 503. Each trigger creates a Job that makes one request through the caller, with a fresh 128-bit attempt ID. The scenario allows at most 20 attempts. `recover` changes the upstream to return HTTP 200.

Build and load the fixture image into a Kind cluster, then run the scenario:

```bash
docker build -f incident-fixture.Dockerfile -t k8os-incident:dev .
kind load docker-image k8os-incident:dev --name YOUR_KIND_CLUSTER
./k8os scenario apply upstream-dependency-v1 --namespace k8os-upstream-1 --image k8os-incident:dev
./k8os scenario trigger upstream-dependency-v1 --namespace k8os-upstream-1 --variant bridge
./k8os scenario trigger upstream-dependency-v1 --namespace k8os-upstream-1 --variant no-bridge
./k8os scenario evidence upstream-dependency-v1 --namespace k8os-upstream-1
./k8os scenario recover upstream-dependency-v1 --namespace k8os-upstream-1
./k8os scenario trigger upstream-dependency-v1 --namespace k8os-upstream-1 --variant recovered
./k8os scenario cleanup upstream-dependency-v1 --namespace k8os-upstream-1
```

`bridge` produces `receiver_accepted`, `receiver_responded`, and `caller_returned` records for the attempt. `no-bridge` produces only `caller_returned`, despite recording an upstream Pod UID. After recovery, the `recovered` variant expects HTTP 200. The caller and upstream write `northstar.request.v1` records; the caller also writes a `northstar.dependency-locator.v1` record identifying the upstream Pod. That locator is a discovery clue, not evidence that the attempt reached the Pod.

`evidence` reads up to 200 lines and 256 KiB of logs per scenario Pod. Its JSON output includes raw log lines, source timestamps, SHA-256 digests, Pod UIDs, image IDs, and acquisition times. Every excerpt is marked as sampled. Capture evidence before recovery if you need logs from the old upstream Pod. The command reads Kubernetes Pod logs directly; it does not establish complete request coverage or retained log collection.

`apply` requires a namespace that does not already exist. `cleanup` deletes the whole scenario namespace only when it has the exact ownership labels. The resource catalog's `cleanup` command has different behavior and keeps its namespace.

## Tests

```bash
go test ./...
./test/kind-upstream.sh
```

The Kind check creates a temporary cluster by default. Set `KIND_CLUSTER=<existing-kind-cluster>` to use an existing cluster; the check then creates and removes its `k8os-upstream-test` namespace.
