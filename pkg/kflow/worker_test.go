package kflow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestDispatch_LocalMode(t *testing.T) {
	old := os.Args
	os.Args = []string{old[0], "--local"}
	defer func() { os.Args = old }()
	t.Setenv("KFLOW_STATE_TOKEN", "")

	wf := New("dispatch-local")
	wf.Task("A", func(_ context.Context, input Input) (Output, error) {
		return Output{"ok": true}, nil
	})
	wf.Flow(Step("A").End())

	if err := Dispatch(wf, Input{}); err != nil {
		t.Fatalf("Dispatch local: %v", err)
	}
}

func TestDispatch_SubmitMode(t *testing.T) {
	var registered, triggered bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/workflows":
			registered = true
			w.WriteHeader(http.StatusCreated)
		case "/api/v1/workflows/dispatch-submit/run":
			triggered = true
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	old := os.Args
	os.Args = []string{old[0]}
	defer func() { os.Args = old }()

	t.Setenv("KFLOW_SERVER", srv.URL)
	t.Setenv("KFLOW_STATE_TOKEN", "")

	wf := New("dispatch-submit")
	wf.Task("A", func(_ context.Context, _ Input) (Output, error) {
		return Output{}, nil
	})
	wf.Flow(Step("A").End())

	if err := Dispatch(wf, Input{"x": 1}); err != nil {
		t.Fatalf("Dispatch submit: %v", err)
	}
	if !registered {
		t.Error("expected workflow registration call")
	}
	if !triggered {
		t.Error("expected workflow trigger call")
	}
}
