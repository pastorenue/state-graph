package k8s

import (
	"errors"
	"fmt"
	"net"
	"net/url"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
)

// clusterError inspects err and prefixes it with a human-readable hint about
// likely cluster connectivity or auth issues. Unrecognised errors are wrapped
// as "k8s: <op>: <err>".
func clusterError(op string, err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		var netErr *net.OpError
		if errors.As(urlErr, &netErr) && netErr.Op == "dial" {
			return fmt.Errorf("k8s: %s: cluster not reachable — is the API server running? (%w)", op, err)
		}
		return fmt.Errorf("k8s: %s: cluster connection error (%w)", op, err)
	}
	if k8serrors.IsUnauthorized(err) {
		return fmt.Errorf("k8s: %s: authentication failed — check kubeconfig credentials (%w)", op, err)
	}
	if k8serrors.IsForbidden(err) {
		return fmt.Errorf("k8s: %s: permission denied — check RBAC rules for the service account (%w)", op, err)
	}
	return fmt.Errorf("k8s: %s: %w", op, err)
}
