package chaos

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	client := fake.NewSimpleDynamicClientWithCustomListKinds(nil, map[schema.GroupVersionResource]string{{Group: "", Version: "v1", Resource: "configmaps"}: "ConfigMapList"}, owned, foreign)
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
