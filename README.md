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
