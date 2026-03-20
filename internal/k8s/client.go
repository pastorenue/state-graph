// Package k8s provides a thin wrapper around client-go for creating and
// managing Kubernetes Jobs and Deployments used by the kflow orchestrator.
// This package must NOT import internal/store or internal/engine.
package k8s

import (
	"fmt"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Client wraps the Kubernetes client-go clientset with project-specific helpers.
type Client struct {
	clientset *kubernetes.Clientset
	namespace string
}

// NewClient creates a K8s client scoped to namespace. Tries in-cluster config
// first (KUBERNETES_SERVICE_HOST present), then falls back to kubeconfig
// (~/.kube/config or KUBECONFIG env var).
func NewClient(namespace string) (*Client, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		// Not running inside a cluster; try kubeconfig.
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			home, _ := os.UserHomeDir()
			kubeconfig = home + "/.kube/config"
		}
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("k8s: cannot build kubeconfig: %w", err)
		}
	}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: cannot create clientset: %w", err)
	}

	return &Client{clientset: cs, namespace: namespace}, nil
}

// Namespace returns the namespace this client is scoped to.
func (c *Client) Namespace() string { return c.namespace }

// Clientset returns the underlying kubernetes.Clientset.
// Used by telemetry.StreamJobLogs to read pod logs.
func (c *Client) Clientset() *kubernetes.Clientset { return c.clientset }
