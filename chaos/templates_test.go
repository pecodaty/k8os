package chaos

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
)

func TestRenderModesAndMetadata(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  Mode
		count int
	}{{"light", Light, 30}, {"moderate", Moderate, 75}, {"hell", Hell, 150}} {
		t.Run(tc.name, func(t *testing.T) {
			objects, err := Render("chaos-lab", tc.mode, map[string]string{"team": "platform"})
			if err != nil {
				t.Fatal(err)
			}
			if len(objects) != tc.count {
				t.Fatalf("got %d objects, want %d", len(objects), tc.count)
			}
			for _, obj := range objects {
				if obj.GetNamespace() != "chaos-lab" {
					t.Errorf("namespace = %q", obj.GetNamespace())
				}
				labels := obj.GetLabels()
				if labels["app.kubernetes.io/name"] != "k8os" || labels["k8os.io/mode"] != string(tc.mode) || labels["team"] != "platform" {
					t.Errorf("labels = %#v", labels)
				}
			}
		})
	}
}

func TestValidation(t *testing.T) {
	if _, err := ParseMode("bad"); err == nil {
		t.Error("invalid mode accepted")
	}
	if err := ValidateNamespace("Bad_Name"); err == nil {
		t.Error("invalid namespace accepted")
	}
	if err := ValidateLabels(map[string]string{"bad key": "x"}); err == nil {
		t.Error("invalid label accepted")
	}
}

func TestCleanupOnlyDeletesOwnedResourcesInNamespace(t *testing.T) {
	owned := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "owned", "namespace": "k8os", "labels": map[string]interface{}{"app.kubernetes.io/name": "k8os", "app.kubernetes.io/managed-by": "k8os"}}}}
	foreign := owned.DeepCopy()
	foreign.SetName("foreign")
	foreign.SetLabels(map[string]string{"app.kubernetes.io/name": "other"})
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), cleanupListKinds(), owned, foreign)
	count, err := Cleanup(context.Background(), client, "k8os")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("deleted %d resources, want 1", count)
	}
	remaining, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace("k8os").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining.Items) != 1 || remaining.Items[0].GetName() != "foreign" {
		t.Fatalf("remaining resources = %#v", remaining.Items)
	}
}

func TestCleanupDeletesJobsWithBackgroundPropagation(t *testing.T) {
	job := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]interface{}{
			"name":      "owned-job",
			"namespace": "k8os",
			"labels": map[string]interface{}{
				"app.kubernetes.io/name":       "k8os",
				"app.kubernetes.io/managed-by": "k8os",
			},
		},
	}}
	client := &recordingDynamicClient{Interface: fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), cleanupListKinds(), job)}

	if _, err := Cleanup(context.Background(), client, "k8os"); err != nil {
		t.Fatal(err)
	}

	if len(client.deleteOptions) != 1 {
		t.Fatalf("delete calls = %d, want 1", len(client.deleteOptions))
	}
	options := client.deleteOptions[0]
	if options.PropagationPolicy == nil {
		t.Fatal("job delete propagation policy is nil")
	}
	if *options.PropagationPolicy != metav1.DeletePropagationBackground {
		t.Fatalf("job delete propagation policy = %q, want %q", *options.PropagationPolicy, metav1.DeletePropagationBackground)
	}
}

func cleanupListKinds() map[schema.GroupVersionResource]string {
	listKinds := make(map[schema.GroupVersionResource]string, len(resources))
	for kind, resourceName := range resources {
		group, version := apiForKind(kind)
		listKinds[schema.GroupVersionResource{Group: group, Version: version, Resource: resourceName}] = kind + "List"
	}
	return listKinds
}

type recordingDynamicClient struct {
	dynamic.Interface
	deleteOptions []metav1.DeleteOptions
}

func (c *recordingDynamicClient) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return &recordingResourceInterface{
		NamespaceableResourceInterface: c.Interface.Resource(gvr),
		recorder:                       c,
	}
}

type recordingResourceInterface struct {
	dynamic.NamespaceableResourceInterface
	recorder *recordingDynamicClient
}

func (r *recordingResourceInterface) Namespace(namespace string) dynamic.ResourceInterface {
	return &recordingNamespacedResourceInterface{
		ResourceInterface: r.NamespaceableResourceInterface.Namespace(namespace),
		recorder:          r.recorder,
	}
}

type recordingNamespacedResourceInterface struct {
	dynamic.ResourceInterface
	recorder *recordingDynamicClient
}

func (r *recordingNamespacedResourceInterface) Delete(ctx context.Context, name string, options metav1.DeleteOptions, subresources ...string) error {
	r.recorder.deleteOptions = append(r.recorder.deleteOptions, options)
	return r.ResourceInterface.Delete(ctx, name, options, subresources...)
}
