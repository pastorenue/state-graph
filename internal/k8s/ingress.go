package k8s

import (
	"context"
	"fmt"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// IngressSpec describes a K8s Ingress for external exposure of a kflow Service.
type IngressSpec struct {
	Name        string // K8s Ingress name (same as K8s Service name)
	ServiceName string // K8s Service to route to
	Port        int32
	Host        string // ingress hostname from ServiceDef.Expose()
	Namespace   string
}

const defaultIngressClass = "nginx"

// CreateIngress creates a K8s Ingress routing external traffic to ServiceName.
func (c *Client) CreateIngress(ctx context.Context, spec IngressSpec) error {
	ns := spec.Namespace
	if ns == "" {
		ns = c.namespace
	}

	ingressClass := defaultIngressClass
	pathType := networkingv1.PathTypePrefix
	port := networkingv1.ServiceBackendPort{Number: spec.Port}

	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      spec.Name,
			Namespace: ns,
			Labels:    map[string]string{"app": "kflow"},
			Annotations: map[string]string{
				"nginx.ingress.kubernetes.io/rewrite-target": "/",
			},
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &ingressClass,
			Rules: []networkingv1.IngressRule{
				{
					Host: spec.Host,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathType,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: spec.ServiceName,
											Port: port,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	if _, err := c.clientset.NetworkingV1().Ingresses(ns).Create(ctx, ingress, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("k8s: create ingress %q: %w", spec.Name, err)
	}
	return nil
}

// DeleteIngress deletes a K8s Ingress by name.
func (c *Client) DeleteIngress(ctx context.Context, name string) error {
	err := c.clientset.NetworkingV1().Ingresses(c.namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("k8s: delete ingress %q: %w", name, err)
	}
	return nil
}
