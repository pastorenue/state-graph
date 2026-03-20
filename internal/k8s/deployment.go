package k8s

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/watch"
)

// DeploymentSpec describes a K8s Deployment + K8s Service pair for a kflow Service.
type DeploymentSpec struct {
	Name      string  // K8s Deployment and K8s Service name (pre-computed via SvcName)
	Image     string  // container image (must include explicit tag)
	Args      []string
	Port      int32
	MinScale  int32
	MaxScale  int32 // initial replica count = MinScale; MaxScale reserved for HPA
	Namespace string
}

// SvcName returns the K8s Deployment/Service name for a kflow Service name.
// Format: "kflow-svc-<service-name-kebab>", capped at 63 characters.
func SvcName(serviceName string) string {
	kebab := toKebab(serviceName)
	name := "kflow-svc-" + kebab
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.TrimRight(name, "-")
}

// CreateDeployment creates a K8s Deployment and a ClusterIP K8s Service.
// Returns when the API server accepts the resources. Use WatchDeploymentRollout
// to wait for rollout completion.
// If K8s Service creation fails after Deployment creation, the orphaned Deployment
// is deleted (best-effort cleanup).
func (c *Client) CreateDeployment(ctx context.Context, spec DeploymentSpec) error {
	ns := spec.Namespace
	if ns == "" {
		ns = c.namespace
	}

	if spec.MinScale < 1 {
		return fmt.Errorf("k8s: Deployment %q MinScale must be >= 1", spec.Name)
	}

	replicas := spec.MinScale
	runAsNonRoot := true
	var runAsUser int64 = 65534

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      spec.Name,
			Namespace: ns,
			Labels:    map[string]string{"app": "kflow", "kflow/service": spec.Name},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"kflow/service": spec.Name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"kflow/service": spec.Name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "service",
							Image: spec.Image,
							Args:  spec.Args,
							Ports: []corev1.ContainerPort{
								{ContainerPort: spec.Port, Protocol: corev1.ProtocolTCP},
							},
							SecurityContext: &corev1.SecurityContext{
								RunAsNonRoot:             &runAsNonRoot,
								RunAsUser:                &runAsUser,
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

	if _, err := c.clientset.AppsV1().Deployments(ns).Create(ctx, deploy, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("k8s: create deployment %q: %w", spec.Name, err)
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      spec.Name,
			Namespace: ns,
			Labels:    map[string]string{"app": "kflow", "kflow/service": spec.Name},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"kflow/service": spec.Name},
			Ports: []corev1.ServicePort{
				{
					Port:       spec.Port,
					TargetPort: intstr.FromInt32(spec.Port),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Type: corev1.ServiceTypeClusterIP,
		},
	}

	if _, err := c.clientset.CoreV1().Services(ns).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		// Best-effort cleanup of orphaned Deployment.
		_ = c.clientset.AppsV1().Deployments(ns).Delete(ctx, spec.Name, metav1.DeleteOptions{})
		return fmt.Errorf("k8s: create service %q (deployment cleaned up): %w", spec.Name, err)
	}

	return nil
}

// UpdateDeploymentReplicas scales the named Deployment to replicas.
func (c *Client) UpdateDeploymentReplicas(ctx context.Context, name string, replicas int32) error {
	deploy, err := c.clientset.AppsV1().Deployments(c.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("k8s: get deployment %q for scale: %w", name, err)
	}
	deploy.Spec.Replicas = &replicas
	if _, err := c.clientset.AppsV1().Deployments(c.namespace).Update(ctx, deploy, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("k8s: update deployment replicas %q: %w", name, err)
	}
	return nil
}

// DeleteDeployment deletes the K8s Deployment and its associated K8s Service.
func (c *Client) DeleteDeployment(ctx context.Context, name string) error {
	prop := metav1.DeletePropagationBackground
	deleteOpts := metav1.DeleteOptions{PropagationPolicy: &prop}

	deployErr := c.clientset.AppsV1().Deployments(c.namespace).Delete(ctx, name, deleteOpts)
	svcErr := c.clientset.CoreV1().Services(c.namespace).Delete(ctx, name, deleteOpts)

	if deployErr != nil {
		return fmt.Errorf("k8s: delete deployment %q: %w", name, deployErr)
	}
	if svcErr != nil {
		return fmt.Errorf("k8s: delete service %q: %w", name, svcErr)
	}
	return nil
}

// GetDeploymentClusterIP returns the ClusterIP of the K8s Service for name.
func (c *Client) GetDeploymentClusterIP(ctx context.Context, name string) (string, error) {
	svc, err := c.clientset.CoreV1().Services(c.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("k8s: get service ClusterIP for %q: %w", name, err)
	}
	if svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == "None" {
		return "", fmt.Errorf("k8s: service %q has no ClusterIP assigned yet", name)
	}
	return svc.Spec.ClusterIP, nil
}

// WatchDeploymentRollout blocks until the named Deployment has at least minReady
// available replicas or the context deadline is exceeded. Uses the Watch API.
func (c *Client) WatchDeploymentRollout(ctx context.Context, name string, minReady int32) error {
	watcher, err := c.clientset.AppsV1().Deployments(c.namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + name,
	})
	if err != nil {
		return fmt.Errorf("k8s: watch deployment %q: %w", name, err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("k8s: watch channel closed before deployment %q became ready", name)
			}
			if event.Type != watch.Modified && event.Type != watch.Added {
				continue
			}
			deploy, ok := event.Object.(*appsv1.Deployment)
			if !ok {
				continue
			}
			if deploy.Status.AvailableReplicas >= minReady {
				return nil
			}
		}
	}
}
