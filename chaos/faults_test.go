package chaos

import (
	"strings"
	"testing"
)

// Every named fault renders owned, scenario-labelled objects; the
// cluster-scoped webhook names its namespace, matches only a group that does
// not exist, and unknown faults are refused.
func TestRenderFaults(t *testing.T) {
	for _, fault := range Faults() {
		objects, err := RenderFault("k8os-e2e", fault, map[string]string{"northstar.io/scenario": "s"})
		if err != nil || len(objects) == 0 {
			t.Fatalf("%s: %v", fault, err)
		}
		for _, o := range objects {
			l := o.GetLabels()
			if l["app.kubernetes.io/managed-by"] != "k8os" || l["k8os.io/fault"] != fault || l["k8os.io/namespace"] != "k8os-e2e" || l["northstar.io/scenario"] != "s" {
				t.Fatalf("%s %s labels: %v", fault, o.GetName(), l)
			}
			if _, ok := resources[o.GetKind()]; !ok {
				t.Fatalf("%s renders unsupported kind %s", fault, o.GetKind())
			}
			if clusterScoped[o.GetKind()] && (o.GetNamespace() != "" || !strings.Contains(o.GetName(), "k8os-e2e")) {
				t.Fatalf("cluster-scoped %s is not namespaced by name: %s/%s", o.GetKind(), o.GetNamespace(), o.GetName())
			}
		}
	}
	webhook, _ := RenderFault("k8os-e2e", "webhook-no-endpoints", nil)
	raw := strings.Join([]string{webhook[1].GetKind(), toString(webhook[1].Object)}, " ")
	if !strings.Contains(raw, "k8os.invalid") {
		t.Fatalf("webhook matches a real group: %s", raw)
	}
	if _, err := RenderFault("k8os-e2e", "delete-everything", nil); err == nil {
		t.Fatal("unknown fault rendered")
	}
}

func toString(v any) string {
	switch t := v.(type) {
	case map[string]interface{}:
		out := ""
		for k, x := range t {
			out += k + ":" + toString(x) + " "
		}
		return out
	case []interface{}:
		out := ""
		for _, x := range t {
			out += toString(x) + " "
		}
		return out
	case string:
		return t
	}
	return ""
}
