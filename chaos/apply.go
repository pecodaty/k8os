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

// Heal updates only owned k8os resources in the namespace with minimal healthy specs.
func Heal(ctx context.Context, client dynamic.Interface, namespace string) (int, error) {
	if err := ValidateNamespace(namespace); err != nil {
		return 0, err
	}
	selector := "app.kubernetes.io/name=k8os,app.kubernetes.io/managed-by=k8os"
	healed := 0
	for kind, resourceName := range resources {
		group, version := apiForKind(kind)
		resource := client.Resource(schema.GroupVersionResource{Group: group, Version: version, Resource: resourceName}).Namespace(namespace)
		list, err := resource.List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return healed, fmt.Errorf("list %s: %w", resourceName, err)
		}
		for i := range list.Items {
			obj := &list.Items[i]
			obj.Object["spec"] = healthySpec(kind)
			labels := obj.GetLabels()
			if labels == nil {
				labels = map[string]string{}
			}
			labels["k8os.io/healed"] = "true"
			obj.SetLabels(labels)
			if _, err := resource.Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
				return healed, fmt.Errorf("heal %s/%s: %w", kind, obj.GetName(), err)
			}
			healed++
		}
	}
	return healed, nil
}

func healthySpec(kind string) map[string]interface{} {
	container := map[string]interface{}{"name": "healthy", "image": "nginx:stable", "ports": []interface{}{map[string]interface{}{"containerPort": int64(80)}}}
	pod := map[string]interface{}{"restartPolicy": "Always", "containers": []interface{}{container}}
	switch kind {
	case "Pod":
		return pod
	case "Deployment", "ReplicaSet":
		return map[string]interface{}{"replicas": int64(1), "selector": map[string]interface{}{"matchLabels": map[string]interface{}{"app": "k8os-healed"}}, "template": map[string]interface{}{"metadata": map[string]interface{}{"labels": map[string]interface{}{"app": "k8os-healed"}}, "spec": pod}}
	case "DaemonSet":
		return map[string]interface{}{"selector": map[string]interface{}{"matchLabels": map[string]interface{}{"app": "k8os-healed"}}, "template": map[string]interface{}{"metadata": map[string]interface{}{"labels": map[string]interface{}{"app": "k8os-healed"}}, "spec": pod}}
	case "StatefulSet":
		return map[string]interface{}{"serviceName": "k8os-healed", "replicas": int64(1), "selector": map[string]interface{}{"matchLabels": map[string]interface{}{"app": "k8os-healed"}}, "template": map[string]interface{}{"metadata": map[string]interface{}{"labels": map[string]interface{}{"app": "k8os-healed"}}, "spec": pod}}
	case "Job":
		pod["restartPolicy"] = "Never"
		return map[string]interface{}{"backoffLimit": int64(1), "template": map[string]interface{}{"spec": pod}}
	case "CronJob":
		pod["restartPolicy"] = "Never"
		return map[string]interface{}{"schedule": "*/5 * * * *", "jobTemplate": map[string]interface{}{"spec": map[string]interface{}{"template": map[string]interface{}{"spec": pod}}}}
	case "Service":
		return map[string]interface{}{"selector": map[string]interface{}{"app": "k8os-healed"}, "ports": []interface{}{map[string]interface{}{"port": int64(80), "targetPort": int64(80)}}}
	case "PersistentVolumeClaim":
		return map[string]interface{}{"accessModes": []interface{}{"ReadWriteOnce"}, "resources": map[string]interface{}{"requests": map[string]interface{}{"storage": "1Gi"}}}
	case "Ingress":
		backend := map[string]interface{}{"service": map[string]interface{}{"name": "k8os-healed", "port": map[string]interface{}{"number": int64(80)}}}
		path := map[string]interface{}{"path": "/", "pathType": "Prefix", "backend": backend}
		return map[string]interface{}{"rules": []interface{}{map[string]interface{}{"http": map[string]interface{}{"paths": []interface{}{path}}}}}
	case "Role":
		return map[string]interface{}{"rules": []interface{}{map[string]interface{}{"apiGroups": []interface{}{""}, "resources": []interface{}{"pods"}, "verbs": []interface{}{"get"}}}}
	case "RoleBinding":
		return map[string]interface{}{"subjects": []interface{}{map[string]interface{}{"kind": "ServiceAccount", "name": "default"}}, "roleRef": map[string]interface{}{"kind": "Role", "name": "k8os-role-1", "apiGroup": "rbac.authorization.k8s.io"}}
	default:
		return nil
	}
}

// Cleanup deletes only resources carrying both k8os ownership labels in the requested namespace.
// The namespace itself is retained so cleanup cannot affect unrelated namespaces.
func Cleanup(ctx context.Context, client dynamic.Interface, namespace string) (int, error) {
	if err := ValidateNamespace(namespace); err != nil {
		return 0, err
	}
	selector := "app.kubernetes.io/name=k8os,app.kubernetes.io/managed-by=k8os"
	propagationPolicy := metav1.DeletePropagationBackground
	deleteOptions := metav1.DeleteOptions{PropagationPolicy: &propagationPolicy}
	deleted := 0
	for kind, resourceName := range resources {
		group, version := apiForKind(kind)
		resource := client.Resource(schema.GroupVersionResource{Group: group, Version: version, Resource: resourceName}).Namespace(namespace)
		list, err := resource.List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return deleted, fmt.Errorf("list %s: %w", resourceName, err)
		}
		for i := range list.Items {
			if err := resource.Delete(ctx, list.Items[i].GetName(), deleteOptions); err != nil && !apierrors.IsNotFound(err) {
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
