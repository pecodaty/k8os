package chaos

import (
	"context"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
)

// faults are named, targeted fixtures, each producing one structural
// condition a platform should notice. Cluster-scoped objects carry the
// k8os.io/namespace label so cleanup removes only the ones this namespace made,
// and a webhook matches an API group that does not exist, so it never admits
// or blocks a real request.
var faults = map[string][]string{
	// A healthy Deployment whose image BumpImage then changes in place.
	"image-bump": {`apiVersion: apps/v1
kind: Deployment
metadata: {name: k8os-image-bump, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
spec:
  replicas: 1
  selector: {matchLabels: {k8os.io/fault: image-bump}}
  template:
    metadata: {labels: {k8os.io/fault: image-bump}}
    spec: {containers: [{name: app, image: "nginx:1.27"}]}
`},
	// A Deployment mounting a ConfigMap that BumpConfig then edits in place.
	"config-bump": {`apiVersion: v1
kind: ConfigMap
metadata: {name: k8os-config, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
data: {mode: "one"}
`, `apiVersion: apps/v1
kind: Deployment
metadata: {name: k8os-config-reader, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
spec:
  replicas: 1
  selector: {matchLabels: {k8os.io/fault: config-bump}}
  template:
    metadata: {labels: {k8os.io/fault: config-bump}}
    spec:
      containers: [{name: app, image: "nginx:1.27", volumeMounts: [{name: config, mountPath: /etc/k8os}]}]
      volumes: [{name: config, configMap: {name: k8os-config}}]
`},
	// A budget that requires every pod it covers to stay up.
	"pdb-impossible": {`apiVersion: apps/v1
kind: Deployment
metadata: {name: k8os-pdb-target, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
spec:
  replicas: 1
  selector: {matchLabels: {k8os.io/fault: pdb-impossible}}
  template:
    metadata: {labels: {k8os.io/fault: pdb-impossible}}
    spec: {containers: [{name: app, image: "nginx:1.27"}]}
`, `apiVersion: policy/v1
kind: PodDisruptionBudget
metadata: {name: k8os-pdb-impossible, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
spec: {minAvailable: 1, selector: {matchLabels: {k8os.io/fault: pdb-impossible}}}
`},
	// Pods whose node selector no node satisfies, so the scheduler rejects every node.
	"unschedulable": {`apiVersion: apps/v1
kind: Deployment
metadata: {name: k8os-unschedulable, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
spec:
  replicas: 2
  selector: {matchLabels: {k8os.io/fault: unschedulable}}
  template:
    metadata: {labels: {k8os.io/fault: unschedulable}}
    spec:
      nodeSelector: {k8os.io/node: impossible}
      containers: [{name: app, image: "nginx:1.27", resources: {requests: {cpu: 100m, memory: 64Mi}}}]
`},
	// A fail-closed webhook whose Service selects nothing.
	"webhook-no-endpoints": {`apiVersion: v1
kind: Service
metadata: {name: k8os-webhook, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
spec: {selector: {k8os.io/fault: webhook-without-pods}, ports: [{port: 443, targetPort: 8443}]}
`, `apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata: {name: k8os-{{NAMESPACE}}-webhook, labels: {{LABELS}}}
webhooks:
- name: never.k8os.invalid
  failurePolicy: Fail
  sideEffects: None
  admissionReviewVersions: [v1]
  clientConfig: {service: {namespace: {{NAMESPACE}}, name: k8os-webhook, port: 443}}
  rules: [{apiGroups: [k8os.invalid], apiVersions: [v1], operations: [CREATE], resources: [nothing], scope: "*"}]
`},
}

// clusterScoped are the kinds k8os creates without a namespace.
var clusterScoped = map[string]bool{"ValidatingWebhookConfiguration": true}

// Faults lists the named faults, sorted.
func Faults() []string {
	out := make([]string, 0, len(faults))
	for name := range faults {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// RenderFault expands one named fault into objects labelled like the catalog.
func RenderFault(namespace, fault string, labels map[string]string) ([]unstructured.Unstructured, error) {
	if err := ValidateNamespace(namespace); err != nil {
		return nil, err
	}
	raw, ok := faults[fault]
	if !ok {
		return nil, fmt.Errorf("unknown fault %q: choose one of %s", fault, strings.Join(Faults(), ", "))
	}
	if err := ValidateLabels(labels); err != nil {
		return nil, err
	}
	labelText := "{"
	for k, v := range labels {
		labelText += fmt.Sprintf("%q: %q, ", k, v)
	}
	labelText += fmt.Sprintf("%q: %q, %q: %q, %q: %q, %q: %q}", "app.kubernetes.io/name", "k8os", "app.kubernetes.io/managed-by", "k8os", "k8os.io/fault", fault, "k8os.io/namespace", namespace)
	out := []unstructured.Unstructured{}
	for _, text := range raw {
		text = strings.ReplaceAll(strings.ReplaceAll(text, "{{NAMESPACE}}", namespace), "{{LABELS}}", labelText)
		var obj map[string]interface{}
		if err := yaml.Unmarshal([]byte(text), &obj); err != nil {
			return nil, err
		}
		u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&unstructured.Unstructured{Object: obj})
		if err != nil {
			return nil, err
		}
		out = append(out, unstructured.Unstructured{Object: u})
	}
	return out, nil
}

// BumpImage changes the first container image of a k8os-owned Deployment in
// place, so a platform can observe an image change on the same UID.
func BumpImage(ctx context.Context, client dynamic.Interface, namespace, name, image string) error {
	if err := ValidateNamespace(namespace); err != nil {
		return err
	}
	resource := client.Resource(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}).Namespace(namespace)
	obj, err := resource.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if obj.GetLabels()["app.kubernetes.io/managed-by"] != "k8os" {
		return fmt.Errorf("deployment %s/%s is not k8os-owned", namespace, name)
	}
	containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if len(containers) == 0 {
		return fmt.Errorf("deployment %s/%s has no containers", namespace, name)
	}
	containers[0].(map[string]interface{})["image"] = image
	if err := unstructured.SetNestedSlice(obj.Object, containers, "spec", "template", "spec", "containers"); err != nil {
		return err
	}
	_, err = resource.Update(ctx, obj, metav1.UpdateOptions{})
	return err
}

// BumpConfig sets one key of a k8os-owned ConfigMap, so a platform can
// observe a configuration change on the same UID.
func BumpConfig(ctx context.Context, client dynamic.Interface, namespace, name, key, value string) error {
	if err := ValidateNamespace(namespace); err != nil {
		return err
	}
	resource := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace(namespace)
	obj, err := resource.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if obj.GetLabels()["app.kubernetes.io/managed-by"] != "k8os" {
		return fmt.Errorf("configmap %s/%s is not k8os-owned", namespace, name)
	}
	if err := unstructured.SetNestedField(obj.Object, value, "data", key); err != nil {
		return err
	}
	_, err = resource.Update(ctx, obj, metav1.UpdateOptions{})
	return err
}
