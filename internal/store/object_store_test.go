package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/pastorenue/kflow/internal/store"
)

func TestObjectStore_PutGet(t *testing.T) {
	uri := os.Getenv("KFLOW_OBJECT_STORE_URI")
	if uri == "" {
		t.Skip("KFLOW_OBJECT_STORE_URI not set — skipping object store integration test")
	}

	ctx := context.Background()
	obj, err := store.NewObjectStore(ctx, uri)
	if err != nil {
		t.Fatalf("NewObjectStore: %v", err)
	}

	key := "test/object_store_put_get.json"
	data := []byte(`{"hello":"world"}`)

	if err := obj.Put(ctx, key, data); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := obj.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("Get returned %q, want %q", got, data)
	}
}

func TestObjectStore_GetNotFound(t *testing.T) {
	uri := os.Getenv("KFLOW_OBJECT_STORE_URI")
	if uri == "" {
		t.Skip("KFLOW_OBJECT_STORE_URI not set — skipping object store integration test")
	}

	ctx := context.Background()
	obj, err := store.NewObjectStore(ctx, uri)
	if err != nil {
		t.Fatalf("NewObjectStore: %v", err)
	}

	_, err = obj.Get(ctx, "test/does-not-exist-at-all.json")
	if err != store.ErrObjectNotFound {
		t.Errorf("expected ErrObjectNotFound, got %v", err)
	}
}
