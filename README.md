# k8os

k8os creates deliberately broken Kubernetes resources in a namespace owned by k8os. It only uses create operations. Existing workloads are not changed or removed.

The default namespace is `k8os`. Each mode creates one fixed manifest template per supported kind, repeated 2, 5, or 10 times:

```bash
k8os inject
k8os inject --mode moderate --k8os-namespace chaos-lab
k8os inject --mode hell --label team=platform --label env=dev
k8os cleanup --k8os-namespace chaos-lab
k8os heal --k8os-namespace chaos-lab
```

`cleanup` removes resources with both k8os ownership labels from the selected namespace. It keeps the namespace and does not inspect or delete resources elsewhere.

`heal` updates those owned resources in place with minimal healthy specifications and adds `k8os.io/healed=true`.

Every generated resource carries `app.kubernetes.io/name=k8os`, `app.kubernetes.io/managed-by=k8os`, `k8os.io/mode`, and any labels supplied with `--label key=value`.

Run the unit tests with:

```bash
go test ./...
```

## Versioned incident scenarios

The `upstream-dependency-v1` scenario runs two instrumented applications in
its own namespace. A trigger sends one request through the caller. `bridge`
records matching `receiver_accepted`, `receiver_responded`, and
`caller_returned` records. `no-bridge` records only `caller_returned` with a
real upstream Pod UID, so a sampled log excerpt cannot claim an upstream
receipt. Each attempt uses a fresh 128-bit identifier. The failure is bounded
to the scenario workloads and returns HTTP 503; `recover` rolls the upstream
to a healthy response.

```bash
docker build -f incident-fixture.Dockerfile -t k8os-incident:dev .
kind load docker-image k8os-incident:dev --name YOUR_KIND_CLUSTER
go build -o ./k8os ./cmd/k8os
./k8os scenario apply upstream-dependency-v1 --namespace k8os-upstream-1 --image k8os-incident:dev
./k8os scenario trigger upstream-dependency-v1 --namespace k8os-upstream-1 --variant bridge
./k8os scenario trigger upstream-dependency-v1 --namespace k8os-upstream-1 --variant no-bridge
./k8os scenario evidence upstream-dependency-v1 --namespace k8os-upstream-1
./k8os scenario recover upstream-dependency-v1 --namespace k8os-upstream-1
./k8os scenario trigger upstream-dependency-v1 --namespace k8os-upstream-1 --variant recovered
./k8os scenario cleanup upstream-dependency-v1 --namespace k8os-upstream-1
```

`evidence` returns bounded raw Pod log lines with source timestamps, digests,
Pod UIDs, image IDs and acquisition times as JSON. It marks every excerpt as sampled.
It is a direct Kubernetes log read, not proof of a retained log connector or
complete request coverage. The caller and upstream lines use the strict
`northstar.request.v1` schema. Caller logs also carry a separate
`northstar.dependency-locator.v1` record with the upstream Pod name and UID;
it is a discovery clue, not proof that the recorded attempt reached that Pod.
Capture evidence before recovery if old
upstream Pod logs are needed.
The namespace must be new for `apply`; cleanup refuses any namespace without
the exact scenario ownership label. Scenario commands honor `KUBECONFIG`.
Run the full Kind check with `./test/kind-upstream.sh`; set
`KIND_CLUSTER=<existing-kind-cluster>` to reuse a development cluster. The
check creates and removes only its owned `k8os-upstream-test` namespace when
reusing a cluster.
