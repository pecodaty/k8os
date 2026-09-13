package chaos

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var resources = map[string]string{
	"Pod": "pods", "Deployment": "deployments", "ReplicaSet": "replicasets", "DaemonSet": "daemonsets", "StatefulSet": "statefulsets", "Job": "jobs", "CronJob": "cronjobs", "Service": "services", "ConfigMap": "configmaps", "Secret": "secrets", "PersistentVolumeClaim": "persistentvolumeclaims", "Ingress": "ingresses", "ServiceAccount": "serviceaccounts", "Role": "roles", "RoleBinding": "rolebindings",
}

// Creator is the only Kubernetes operation needed by the injector.
type Creator interface {
	Create(context.Context, *unstructured.Unstructured) error
}

// Cleanup deletes only resources carrying both k8os ownership labels in the requested namespace.
// The namespace itself is retained so cleanup cannot affect unrelated namespaces.
func Cleanup(ctx context.Context, client dynamic.Interface, namespace string) (int, error) {
	if err := ValidateNamespace(namespace); err != nil {
		return 0, err
	}
	selector := "app.kubernetes.io/name=k8os,app.kubernetes.io/managed-by=k8os"
	deleted := 0
	for kind, resourceName := range resources {
		group, version := apiForKind(kind)
		resource := client.Resource(schema.GroupVersionResource{Group: group, Version: version, Resource: resourceName}).Namespace(namespace)
		list, err := resource.List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return deleted, fmt.Errorf("list %s: %w", resourceName, err)
		}
		for i := range list.Items {
			if err := resource.Delete(ctx, list.Items[i].GetName(), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return deleted, fmt.Errorf("delete %s/%s: %w", kind, list.Items[i].GetName(), err)
			}
			deleted++
		}
	}
	return deleted, nil
}

func apiForKind(kind string) (string, string) {
	switch kind {
	case "Deployment", "ReplicaSet", "DaemonSet", "StatefulSet":
		return "apps", "v1"
	case "Job", "CronJob":
		return "batch", "v1"
	case "Ingress":
		return "networking.k8s.io", "v1"
	case "Role", "RoleBinding":
		return "rbac.authorization.k8s.io", "v1"
	default:
		return "", "v1"
	}
}

type dynamicCreator struct {
	client    dynamic.Interface
	namespace string
}

func (c dynamicCreator) Create(ctx context.Context, obj *unstructured.Unstructured) error {
	gvr, ok := resources[obj.GetKind()]
	if !ok {
		return fmt.Errorf("unsupported kind %q", obj.GetKind())
	}
	group, version := apiForKind(obj.GetKind())
	resource := c.client.Resource(schema.GroupVersionResource{Group: group, Version: version, Resource: gvr})
	_, err := resource.Namespace(c.namespace).Create(ctx, obj, metav1.CreateOptions{})
	return err
}

// Apply creates the namespace and every rendered object. It never updates or deletes.
func Apply(ctx context.Context, client dynamic.Interface, namespace string, objects []unstructured.Unstructured) error {
	labels := map[string]interface{}{
		"app.kubernetes.io/name":       "k8os",
		"app.kubernetes.io/managed-by": "k8os",
	}
	if len(objects) > 0 {
		for key, value := range objects[0].GetLabels() {
			labels[key] = value
		}
	}
	ns := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]interface{}{"name": namespace, "labels": map[string]interface{}{"app.kubernetes.io/name": "k8os", "app.kubernetes.io/managed-by": "k8os"}}}}
	ns.Object["metadata"].(map[string]interface{})["labels"] = labels
	if _, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Create(ctx, ns, metav1.CreateOptions{}); err != nil && !isAlreadyExists(err) {
		return fmt.Errorf("create namespace: %w", err)
	}
	creator := dynamicCreator{client: client, namespace: namespace}
	for i := range objects {
		if err := creator.Create(ctx, &objects[i]); err != nil {
			return fmt.Errorf("create %s/%s: %w", objects[i].GetKind(), objects[i].GetName(), err)
		}
	}
	return nil
}

func isAlreadyExists(err error) bool {
	return apierrors.IsAlreadyExists(err) || (err != nil && strings.Contains(err.Error(), "already exists"))
}
