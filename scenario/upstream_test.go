package scenario

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestWorkloadContract pins the Pod UID binding and distinct workload selectors.
func TestWorkloadContract(t *testing.T) {
	upstream := deployment("incident", "upstream", "fixture:test", "true")
	caller := deployment("incident", "caller", "fixture:test", "true")
	if upstream.Spec.Selector.MatchLabels[componentLabel] != "upstream" || caller.Spec.Selector.MatchLabels[componentLabel] != "caller" {
		t.Fatal("workload selectors overlap")
	}
	vars := upstream.Spec.Template.Spec.Containers[0].Env
	foundUID := false
	for _, variable := range vars {
		if variable.Name == "POD_UID" && variable.ValueFrom != nil && variable.ValueFrom.FieldRef != nil && variable.ValueFrom.FieldRef.FieldPath == "metadata.uid" {
			foundUID = true
		}
	}
	if !foundUID {
		t.Fatal("upstream does not bind records to its Pod UID")
	}
	if upstream.Labels["app.kubernetes.io/name"] == "k8os" {
		t.Fatal("scenario labels overlap the broad chaos cleanup selector")
	}
}

// TestCleanupRequiresOwnership pins the exclusive-namespace cleanup boundary.
func TestCleanupRequiresOwnership(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "foreign"}})
	if err := (Run{Namespace: "foreign"}).Cleanup(context.Background(), client); err == nil {
		t.Fatal("cleanup accepted an unowned namespace")
	}
	if _, err := client.CoreV1().Namespaces().Get(context.Background(), "foreign", metav1.GetOptions{}); err != nil {
		t.Fatalf("foreign namespace was deleted: %v", err)
	}
	client = fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "owned", Labels: labels("namespace")}})
	if err := (Run{Namespace: "owned"}).Cleanup(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Namespaces().Get(context.Background(), "owned", metav1.GetOptions{}); err == nil {
		t.Fatal("owned namespace remains")
	}
}

// TestAttemptReservation pins the per-run limit before any Job can be created.
func TestAttemptReservation(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "scenario-state", Namespace: "owned"}, Data: map[string]string{"attempts": "19"}})
	run := Run{Namespace: "owned"}
	if err := run.reserveAttempt(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	if err := run.reserveAttempt(context.Background(), client); err == nil {
		t.Fatal("attempt limit was exceeded")
	}
	state, err := client.CoreV1().ConfigMaps("owned").Get(context.Background(), "scenario-state", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if state.Data["attempts"] != "20" {
		t.Fatalf("attempt count = %q", state.Data["attempts"])
	}
}
