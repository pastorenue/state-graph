package k8s

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

// JobSpec describes a Kubernetes Job to be created for a single state execution.
type JobSpec struct {
	// Name is the K8s job name. Must be DNS-safe and <= 63 characters.
	Name string

	// Image is the container image. Must include an explicit tag (never :latest).
	Image string

	// Args are command-line arguments for the container (e.g. ["--state=ValidateOrder"]).
	Args []string

	// Env is the list of environment variables injected into the container.
	Env []EnvVar

	// Namespace overrides the client's default namespace (optional).
	Namespace string
}

// EnvVar is a name-value pair for a container environment variable.
type EnvVar struct {
	Name  string
	Value string
}

// JobResult holds the outcome of a completed Kubernetes Job.
type JobResult struct {
	Succeeded bool
	Failed    bool
	Message   string
}

var nonAlphanumRe = regexp.MustCompile(`[^a-z0-9]+`)

// JobName produces a deterministic, DNS-safe Kubernetes Job name.
// Format: "kflow-<execID[:16]>-<stateName-kebab>", capped at 63 characters.
func JobName(execID, stateName string) string {
	if len(execID) > 16 {
		execID = execID[:16]
	}
	// Strip hyphens from UUID-formatted execIDs
	execID = strings.ReplaceAll(execID, "-", "")
	if len(execID) > 16 {
		execID = execID[:16]
	}

	kebab := toKebab(stateName)
	name := "kflow-" + execID + "-" + kebab

	if len(name) > 63 {
		name = name[:63]
	}
	return strings.TrimRight(name, "-")
}

func toKebab(s string) string {
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		if unicode.IsUpper(r) {
			// Insert hyphen before an uppercase letter when:
			//   - it's not the first character, AND
			//   - either the previous char was lowercase/digit, OR
			//     the next char is lowercase (e.g. "XMLParser" → "xml-parser")
			if i > 0 {
				prev := runes[i-1]
				next := rune(0)
				if i+1 < len(runes) {
					next = runes[i+1]
				}
				if unicode.IsLower(prev) || unicode.IsDigit(prev) ||
					(unicode.IsUpper(prev) && unicode.IsLower(next)) {
					b.WriteByte('-')
				}
			}
			b.WriteRune(unicode.ToLower(r))
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return nonAlphanumRe.ReplaceAllString(b.String(), "-")
}

// CreateJob creates a Kubernetes Job from spec. RestartPolicy is always Never.
// Returns the actual job name used.
func (c *Client) CreateJob(ctx context.Context, spec JobSpec) (string, error) {
	ns := spec.Namespace
	if ns == "" {
		ns = c.namespace
	}

	name := spec.Name
	if name == "" {
		return "", fmt.Errorf("k8s: JobSpec.Name must not be empty")
	}

	envVars := make([]corev1.EnvVar, len(spec.Env))
	for i, e := range spec.Env {
		envVars[i] = corev1.EnvVar{Name: e.Name, Value: e.Value}
	}

	restartNever := corev1.RestartPolicyNever
	runAsNonRoot := true
	var runAsUser int64 = 65534
	readOnly := true

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{"app": "kflow", "kflow/job": "state"},
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: restartNever,
					Containers: []corev1.Container{
						{
							Name:  "runner",
							Image: spec.Image,
							Args:  spec.Args,
							Env:   envVars,
							SecurityContext: &corev1.SecurityContext{
								RunAsNonRoot:             &runAsNonRoot,
								RunAsUser:                &runAsUser,
								ReadOnlyRootFilesystem:   &readOnly,
								AllowPrivilegeEscalation: boolPtr(false),
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
							},
						},
					},
				},
			},
		},
	}

	if _, err := c.clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("k8s: create job %q: %w", name, err)
	}
	return name, nil
}

// DeleteJob deletes a Kubernetes Job by name. Non-fatal if the job is not found.
func (c *Client) DeleteJob(ctx context.Context, name string) error {
	prop := metav1.DeletePropagationBackground
	err := c.clientset.BatchV1().Jobs(c.namespace).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &prop,
	})
	if err != nil {
		return fmt.Errorf("k8s: delete job %q: %w", name, err)
	}
	return nil
}

// WaitForJob blocks until the Job reaches a terminal condition using the Watch
// API. No polling — uses Watch event stream. The caller's context controls the
// deadline.
func (c *Client) WaitForJob(ctx context.Context, name string) (JobResult, error) {
	watcher, err := c.clientset.BatchV1().Jobs(c.namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + name,
	})
	if err != nil {
		return JobResult{}, fmt.Errorf("k8s: watch job %q: %w", name, err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return JobResult{}, ctx.Err()
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return JobResult{}, fmt.Errorf("k8s: watch channel closed before job %q reached terminal state", name)
			}
			if event.Type != watch.Modified && event.Type != watch.Added {
				continue
			}
			job, ok := event.Object.(*batchv1.Job)
			if !ok {
				continue
			}
			for _, cond := range job.Status.Conditions {
				if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
					return JobResult{Succeeded: true}, nil
				}
				if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
					return JobResult{Failed: true, Message: cond.Message}, nil
				}
			}
		}
	}
}

func boolPtr(b bool) *bool { return &b }
