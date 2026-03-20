package k8s

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestClusterError(t *testing.T) {
	t.Run("dial error → cluster not reachable", func(t *testing.T) {
		inner := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		wrapped := &url.Error{Op: "Post", URL: "https://192.168.49.2:8443/", Err: inner}
		got := clusterError("create job", wrapped)
		if !strings.Contains(got.Error(), "cluster not reachable") {
			t.Errorf("expected 'cluster not reachable', got: %v", got)
		}
	})

	t.Run("non-dial url.Error → cluster connection error", func(t *testing.T) {
		wrapped := &url.Error{Op: "Get", URL: "https://192.168.49.2:8443/", Err: errors.New("tls handshake failed")}
		got := clusterError("list pods", wrapped)
		if !strings.Contains(got.Error(), "cluster connection error") {
			t.Errorf("expected 'cluster connection error', got: %v", got)
		}
	})

	t.Run("unauthorized → authentication failed", func(t *testing.T) {
		err := k8serrors.NewUnauthorized("invalid credentials")
		got := clusterError("do thing", err)
		if !strings.Contains(got.Error(), "authentication failed") {
			t.Errorf("expected 'authentication failed', got: %v", got)
		}
	})

	t.Run("forbidden → permission denied", func(t *testing.T) {
		err := k8serrors.NewForbidden(schema.GroupResource{}, "some-resource", errors.New("forbidden"))
		got := clusterError("do thing", err)
		if !strings.Contains(got.Error(), "permission denied") {
			t.Errorf("expected 'permission denied', got: %v", got)
		}
	})

	t.Run("plain error → pass-through", func(t *testing.T) {
		got := clusterError("do thing", errors.New("boom"))
		want := "k8s: do thing: boom"
		if got.Error() != want {
			t.Errorf("expected %q, got: %v", want, got)
		}
	})
}
