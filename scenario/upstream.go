// Package scenario runs bounded, namespace-owned incidents against real Kubernetes workloads.
package scenario

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	intstrutil "k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

const (
	// UpstreamDependencyV1 is the first versioned k8os incident recipe.
	UpstreamDependencyV1 = "upstream-dependency-v1"
	ownerLabel           = "k8os.io/scenario"
	componentLabel       = "k8os.io/component"
	maxAttempts          = 20
)

// Run identifies a disposable scenario namespace and its fixture image.
type Run struct {
	Namespace string
	Image     string
}

// TriggerResult describes one attempted request and its expected source records.
type TriggerResult struct {
	Scenario       string   `json:"scenario"`
	Namespace      string   `json:"namespace"`
	Attempt        string   `json:"attempt"`
	Variant        string   `json:"variant"`
	ExpectedStages []string `json:"expected_stages"`
}

// PodEvidence records a Pod identity and bounded raw application logs.
type PodEvidence struct {
	Name       string    `json:"name"`
	UID        string    `json:"uid"`
	Component  string    `json:"component"`
	ImageID    string    `json:"image_id"`
	AcquiredAt time.Time `json:"acquired_at"`
	Sampled    bool      `json:"sampled"`
	Lines      []LogLine `json:"lines"`
}

// LogLine retains the source timestamp and digest of one raw application line.
type LogLine struct {
	At     time.Time `json:"at"`
	Line   string    `json:"line"`
	SHA256 string    `json:"sha256"`
}

func (r Run) validate() error {
	if len(r.Namespace) < 1 || len(r.Namespace) > 63 {
		return fmt.Errorf("invalid namespace %q", r.Namespace)
	}
	for i, c := range r.Namespace {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && (c != '-' || i == 0 || i == len(r.Namespace)-1) {
			return fmt.Errorf("invalid namespace %q", r.Namespace)
		}
	}
	return nil
}

func labels(component string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "k8os-scenario",
		"app.kubernetes.io/managed-by": "k8os",
		ownerLabel:                     UpstreamDependencyV1,
		componentLabel:                 component,
	}
}

func env(name, value string) corev1.EnvVar { return corev1.EnvVar{Name: name, Value: value} }

func deployment(namespace, component, image, fail string) *appsv1.Deployment {
	selector := map[string]string{ownerLabel: UpstreamDependencyV1, componentLabel: component}
	vars := []corev1.EnvVar{env("ROLE", component)}
	if component == "upstream" {
		vars = append(vars, env("FAIL", fail),
			corev1.EnvVar{Name: "POD_UID", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}}},
			corev1.EnvVar{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
			corev1.EnvVar{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}})
	} else {
		vars = append(vars, env("UPSTREAM_URL", "http://upstream:8080"))
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: component, Namespace: namespace, Labels: labels(component)},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr(int32(1)), Selector: &metav1.LabelSelector{MatchLabels: selector},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels(component)},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name: "fixture", Image: image, ImagePullPolicy: corev1.PullIfNotPresent,
					Env: vars, Ports: []corev1.ContainerPort{{ContainerPort: 8080}},
					ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr(8080)}}, InitialDelaySeconds: 1, PeriodSeconds: 1},
				}}},
			},
		},
	}
}

func ptr[T any](v T) *T { return &v }

func intstr(port int) intstrutil.IntOrString { return intstrutil.FromInt(port) }

// Apply creates the isolated workload pair and waits for both applications to become ready.
func (r Run) Apply(ctx context.Context, client kubernetes.Interface) error {
	if err := r.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.Image) == "" {
		return errors.New("fixture image is required")
	}
	_, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: r.Namespace, Labels: labels("namespace")}}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create exclusive scenario namespace: %w", err)
	}
	_, err = client.CoreV1().ConfigMaps(r.Namespace).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "scenario-state", Namespace: r.Namespace, Labels: labels("state")}, Data: map[string]string{"attempts": "0"}}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create scenario state: %w", err)
	}
	for _, component := range []string{"upstream", "caller"} {
		if _, err := client.AppsV1().Deployments(r.Namespace).Create(ctx, deployment(r.Namespace, component, r.Image, "true"), metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create %s: %w", component, err)
		}
		service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: component, Namespace: r.Namespace, Labels: labels(component)}, Spec: corev1.ServiceSpec{Selector: map[string]string{ownerLabel: UpstreamDependencyV1, componentLabel: component}, Ports: []corev1.ServicePort{{Port: 8080, TargetPort: intstr(8080)}}}}
		if _, err := client.CoreV1().Services(r.Namespace).Create(ctx, service, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create %s service: %w", component, err)
		}
	}
	return wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		for _, component := range []string{"upstream", "caller"} {
			d, err := client.AppsV1().Deployments(r.Namespace).Get(ctx, component, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			if d.Status.AvailableReplicas != 1 || d.Status.ObservedGeneration < d.Generation {
				return false, nil
			}
		}
		return true, nil
	})
}

// Trigger creates one bounded Job that makes an attempt through the caller.
func (r Run) Trigger(ctx context.Context, client kubernetes.Interface, variant string) (TriggerResult, error) {
	if err := r.owned(ctx, client); err != nil {
		return TriggerResult{}, err
	}
	if variant != "bridge" && variant != "no-bridge" && variant != "recovered" {
		return TriggerResult{}, fmt.Errorf("unsupported variant %q", variant)
	}
	d, err := client.AppsV1().Deployments(r.Namespace).Get(ctx, "caller", metav1.GetOptions{})
	if err != nil {
		return TriggerResult{}, err
	}
	image := d.Spec.Template.Spec.Containers[0].Image
	if err := r.reserveAttempt(ctx, client); err != nil {
		return TriggerResult{}, err
	}
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return TriggerResult{}, err
	}
	attempt := hex.EncodeToString(bytes[:])
	name := "attempt-" + attempt[:12]
	requestVariant, expectedStatus := variant, "503"
	if variant == "recovered" {
		requestVariant, expectedStatus = "bridge", "200"
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: r.Namespace, Labels: labels("trigger")},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr(int32(0)), ActiveDeadlineSeconds: ptr(int64(60)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels("trigger")},
				Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{
					Name: "fixture", Image: image, ImagePullPolicy: corev1.PullIfNotPresent,
					Command: []string{"/fixture", "--emit"},
					Env:     []corev1.EnvVar{env("ATTEMPT", attempt), env("VARIANT", requestVariant), env("CALLER_URL", "http://caller:8080"), env("EXPECTED_STATUS", expectedStatus)},
				}}},
			},
		},
	}
	if _, err := client.BatchV1().Jobs(r.Namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return TriggerResult{}, err
	}
	if err := wait.PollUntilContextTimeout(ctx, time.Second, 90*time.Second, true, func(ctx context.Context) (bool, error) {
		j, err := client.BatchV1().Jobs(r.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if j.Status.Failed > 0 {
			return false, fmt.Errorf("attempt job %s failed", name)
		}
		return j.Status.Succeeded > 0, nil
	}); err != nil {
		return TriggerResult{}, err
	}
	stages := []string{}
	if variant != "recovered" {
		stages = []string{"caller_returned"}
	}
	if variant == "bridge" {
		stages = append([]string{"receiver_accepted", "receiver_responded"}, stages...)
	}
	return TriggerResult{Scenario: UpstreamDependencyV1, Namespace: r.Namespace, Attempt: attempt, Variant: variant, ExpectedStages: stages}, nil
}

func (r Run) reserveAttempt(ctx context.Context, client kubernetes.Interface) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		state, err := client.CoreV1().ConfigMaps(r.Namespace).Get(ctx, "scenario-state", metav1.GetOptions{})
		if err != nil {
			return err
		}
		count, err := strconv.Atoi(state.Data["attempts"])
		if err != nil || count < 0 {
			return fmt.Errorf("invalid scenario attempt counter")
		}
		if count >= maxAttempts {
			return fmt.Errorf("scenario attempt limit %d reached", maxAttempts)
		}
		state.Data["attempts"] = strconv.Itoa(count + 1)
		_, err = client.CoreV1().ConfigMaps(r.Namespace).Update(ctx, state, metav1.UpdateOptions{})
		return err
	})
}

// Recover disables the upstream fault and waits for the replacement Pod to become ready.
func (r Run) Recover(ctx context.Context, client kubernetes.Interface) error {
	if err := r.owned(ctx, client); err != nil {
		return err
	}
	d, err := client.AppsV1().Deployments(r.Namespace).Get(ctx, "upstream", metav1.GetOptions{})
	if err != nil {
		return err
	}
	for i := range d.Spec.Template.Spec.Containers[0].Env {
		if d.Spec.Template.Spec.Containers[0].Env[i].Name == "FAIL" {
			d.Spec.Template.Spec.Containers[0].Env[i].Value = "false"
		}
	}
	updated, err := client.AppsV1().Deployments(r.Namespace).Update(ctx, d, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	return wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		d, err := client.AppsV1().Deployments(r.Namespace).Get(ctx, "upstream", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return d.Status.ObservedGeneration >= updated.Generation && d.Status.UpdatedReplicas == 1 && d.Status.AvailableReplicas == 1 && d.Status.Replicas == 1, nil
	})
}

// Evidence returns bounded Pod logs with Kubernetes Pod UIDs for source provenance.
func (r Run) Evidence(ctx context.Context, client kubernetes.Interface) ([]PodEvidence, error) {
	if err := r.owned(ctx, client); err != nil {
		return nil, err
	}
	pods, err := client.CoreV1().Pods(r.Namespace).List(ctx, metav1.ListOptions{LabelSelector: ownerLabel + "=" + UpstreamDependencyV1})
	if err != nil {
		return nil, err
	}
	var out []PodEvidence
	for _, pod := range pods.Items {
		if pod.Labels[componentLabel] == "trigger" {
			continue
		}
		stream, err := client.CoreV1().Pods(r.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{TailLines: ptr(int64(200)), LimitBytes: ptr(int64(256 * 1024)), Timestamps: true}).Stream(ctx)
		if err != nil {
			return nil, fmt.Errorf("read %s logs: %w", pod.Name, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(stream, 256*1024))
		closeErr := stream.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		imageID := ""
		if len(pod.Status.ContainerStatuses) > 0 {
			imageID = pod.Status.ContainerStatuses[0].ImageID
		}
		item := PodEvidence{Name: pod.Name, UID: string(pod.UID), Component: pod.Labels[componentLabel], ImageID: imageID, AcquiredAt: time.Now().UTC(), Sampled: true}
		for _, raw := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			if raw == "" {
				continue
			}
			stamp, line, ok := strings.Cut(raw, " ")
			if !ok {
				return nil, fmt.Errorf("missing timestamp in %s logs", pod.Name)
			}
			at, err := time.Parse(time.RFC3339Nano, stamp)
			if err != nil {
				return nil, fmt.Errorf("parse %s log timestamp: %w", pod.Name, err)
			}
			digest := sha256.Sum256([]byte(line))
			item.Lines = append(item.Lines, LogLine{At: at, Line: line, SHA256: hex.EncodeToString(digest[:])})
		}
		out = append(out, item)
	}
	return out, nil
}

// Cleanup removes only a namespace bearing the exact scenario ownership label.
func (r Run) Cleanup(ctx context.Context, client kubernetes.Interface) error {
	if err := r.owned(ctx, client); err != nil {
		return err
	}
	return client.CoreV1().Namespaces().Delete(ctx, r.Namespace, metav1.DeleteOptions{})
}

func (r Run) owned(ctx context.Context, client kubernetes.Interface) error {
	if err := r.validate(); err != nil {
		return err
	}
	ns, err := client.CoreV1().Namespaces().Get(ctx, r.Namespace, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if ns.Labels[ownerLabel] != UpstreamDependencyV1 || ns.Labels["app.kubernetes.io/name"] != "k8os-scenario" || ns.Labels["app.kubernetes.io/managed-by"] != "k8os" {
		return fmt.Errorf("namespace %q is not owned by %s", r.Namespace, UpstreamDependencyV1)
	}
	return nil
}
