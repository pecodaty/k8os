// Package chaos contains the safe, namespace-scoped resource catalog used by k8os.
package chaos

import (
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const DefaultNamespace = "k8os"

var validNamespace = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

var templates = []string{
	`apiVersion: v1
kind: Pod
metadata:
  name: k8os-pod-{{INDEX}}
  namespace: {{NAMESPACE}}
  labels: {{LABELS}}
spec:
  restartPolicy: Never
  containers:
  - name: broken
    image: k8os.invalid/missing:never
    readinessProbe: {httpGet: {path: /never, port: 1}}
`,
	`apiVersion: apps/v1
kind: Deployment
metadata:
  name: k8os-deployment-{{INDEX}}
  namespace: {{NAMESPACE}}
  labels: {{LABELS}}
spec:
  replicas: 1
  selector: {matchLabels: {k8os.io/template: never}}
  template:
    metadata: {labels: {k8os.io/template: never}}
    spec:
      containers: [{name: broken, image: k8os.invalid/missing:never}]
`,
	`apiVersion: apps/v1
kind: ReplicaSet
metadata: {name: k8os-replicaset-{{INDEX}}, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
spec:
  selector: {matchLabels: {k8os.io/template: never}}
  template: {metadata: {labels: {k8os.io/template: never}}, spec: {containers: [{name: broken, image: k8os.invalid/missing:never}]}}
`,
	`apiVersion: apps/v1
kind: DaemonSet
metadata: {name: k8os-daemonset-{{INDEX}}, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
spec:
  selector: {matchLabels: {k8os.io/template: never}}
  template: {metadata: {labels: {k8os.io/template: never}}, spec: {nodeSelector: {k8os.io/node: impossible}, containers: [{name: broken, image: k8os.invalid/missing:never}]}}
`,
	`apiVersion: apps/v1
kind: StatefulSet
metadata: {name: k8os-statefulset-{{INDEX}}, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
spec:
  serviceName: k8os-missing
  replicas: 1
  selector: {matchLabels: {k8os.io/template: never}}
  template: {metadata: {labels: {k8os.io/template: never}}, spec: {containers: [{name: broken, image: k8os.invalid/missing:never}]}}
`,
	`apiVersion: batch/v1
kind: Job
metadata: {name: k8os-job-{{INDEX}}, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
spec: {backoffLimit: 0, template: {metadata: {labels: {k8os.io/template: never}}, spec: {restartPolicy: Never, containers: [{name: broken, image: k8os.invalid/missing:never}]}}}
`,
	`apiVersion: batch/v1
kind: CronJob
metadata: {name: k8os-cronjob-{{INDEX}}, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
spec: {schedule: "*/5 * * * *", jobTemplate: {spec: {template: {spec: {restartPolicy: Never, containers: [{name: broken, image: k8os.invalid/missing:never}]}}}}}
`,
	`apiVersion: v1
kind: Service
metadata: {name: k8os-service-{{INDEX}}, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
spec: {selector: {k8os.io/template: no-pod}, ports: [{port: 80, targetPort: 8080}]}
`,
	`apiVersion: v1
kind: ConfigMap
metadata: {name: k8os-configmap-{{INDEX}}, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
data: {broken: intentional-chaos}
`,
	`apiVersion: v1
kind: Secret
metadata: {name: k8os-secret-{{INDEX}}, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
type: Opaque
data: {broken: aW50ZW50aW9uYWwtY2hhb3M=}
`,
	`apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: k8os-pvc-{{INDEX}}, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
spec: {accessModes: [ReadWriteOnce], storageClassName: k8os-missing-storage-class, resources: {requests: {storage: 1Gi}}}
`,
	`apiVersion: networking.k8s.io/v1
kind: Ingress
metadata: {name: k8os-ingress-{{INDEX}}, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
spec: {rules: [{host: k8os.invalid, http: {paths: [{path: /, pathType: Prefix, backend: {service: {name: k8os-missing, port: {number: 80}}}}]}}]}
`,
	`apiVersion: v1
kind: ServiceAccount
metadata: {name: k8os-serviceaccount-{{INDEX}}, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
`,
	`apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: k8os-role-{{INDEX}}, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
rules: [{apiGroups: [""], resources: [pods], verbs: [invalid-verb]}]
`,
	`apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: k8os-rolebinding-{{INDEX}}, namespace: {{NAMESPACE}}, labels: {{LABELS}}}
subjects: [{kind: ServiceAccount, name: k8os-missing-account}]
roleRef: {kind: Role, name: k8os-missing-role, apiGroup: rbac.authorization.k8s.io}
`,
}

type Mode string

const (
	Light    Mode = "light"
	Moderate Mode = "moderate"
	Hell     Mode = "hell"
)

func (m Mode) Count() int {
	switch m {
	case Light:
		return 2
	case Moderate:
		return 5
	case Hell:
		return 10
	}
	return 0
}

func ValidateNamespace(ns string) error {
	if ns == "" || len(ns) > 63 || !validNamespace.MatchString(ns) {
		return fmt.Errorf("invalid Kubernetes namespace %q", ns)
	}
	return nil
}

func ParseMode(value string) (Mode, error) {
	m := Mode(value)
	if m.Count() == 0 {
		return "", fmt.Errorf("invalid mode %q: choose light, moderate, or hell", value)
	}
	return m, nil
}

func ValidateLabels(labels map[string]string) error {
	for k, v := range labels {
		if strings.TrimSpace(k) == "" || strings.ContainsAny(k, " \t\n") || strings.ContainsAny(v, "\n") {
			return fmt.Errorf("invalid label %q", k)
		}
	}
	return nil
}

// Render expands the fixed catalog into namespace-scoped Kubernetes objects.
func Render(namespace string, mode Mode, labels map[string]string) ([]unstructured.Unstructured, error) {
	if err := ValidateNamespace(namespace); err != nil {
		return nil, err
	}
	if mode.Count() == 0 {
		return nil, fmt.Errorf("invalid mode %q", mode)
	}
	if err := ValidateLabels(labels); err != nil {
		return nil, err
	}
	labelText := "{"
	for k, v := range labels {
		labelText += fmt.Sprintf("%q: %q, ", k, v)
	}
	labelText += fmt.Sprintf("%q: %q, %q: %q, %q: %q}", "app.kubernetes.io/name", "k8os", "app.kubernetes.io/managed-by", "k8os", "k8os.io/mode", mode)
	var out []unstructured.Unstructured
	for _, raw := range templates {
		for i := 1; i <= mode.Count(); i++ {
			text := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(raw, "{{NAMESPACE}}", namespace), "{{INDEX}}", fmt.Sprint(i)), "{{LABELS}}", labelText)
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
	}
	return out, nil
}
