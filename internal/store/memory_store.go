package store

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/pastorenue/kflow/pkg/kflow"
)

var _ Store = (*MemoryStore)(nil)

type MemoryStore struct {
	mu         sync.RWMutex
	executions map[string]*ExecutionRecord
	states     map[string]*StateRecord // key: execID+":"+stateName
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		executions: make(map[string]*ExecutionRecord),
		states:     make(map[string]*StateRecord),
	}
}

func stateKey(execID, stateName string) string {
	return execID + ":" + stateName
}

func (m *MemoryStore) CreateExecution(ctx context.Context, record ExecutionRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.executions[record.ID]; exists {
		return fmt.Errorf("store: execution %q already exists", record.ID)
	}
	now := time.Now()
	record.Status = StatusPending
	record.CreatedAt = now
	record.UpdatedAt = now
	cp := record
	m.executions[record.ID] = &cp
	return nil
}

func (m *MemoryStore) GetExecution(ctx context.Context, execID string) (ExecutionRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.executions[execID]
	if !ok {
		return ExecutionRecord{}, ErrExecutionNotFound
	}
	return *rec, nil
}

func (m *MemoryStore) UpdateExecution(ctx context.Context, execID string, status Status) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.executions[execID]
	if !ok {
		return ErrExecutionNotFound
	}
	rec.Status = status
	rec.UpdatedAt = time.Now()
	return nil
}

func (m *MemoryStore) WriteAheadState(ctx context.Context, record StateRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := stateKey(record.ExecutionID, record.StateName)
	if existing, ok := m.states[key]; ok {
		if existing.Status == StatusCompleted {
			return ErrStateAlreadyTerminal
		}
	}
	now := time.Now()
	record.Status = StatusPending
	record.CreatedAt = now
	record.UpdatedAt = now
	cp := record
	m.states[key] = &cp
	return nil
}

func (m *MemoryStore) MarkRunning(ctx context.Context, execID, stateName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := stateKey(execID, stateName)
	rec, ok := m.states[key]
	if !ok {
		return ErrStateNotFound
	}
	rec.Status = StatusRunning
	rec.UpdatedAt = time.Now()
	return nil
}

func (m *MemoryStore) CompleteState(ctx context.Context, execID, stateName string, output kflow.Output) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := stateKey(execID, stateName)
	rec, ok := m.states[key]
	if !ok {
		return ErrStateNotFound
	}
	rec.Status = StatusCompleted
	rec.Output = output
	rec.UpdatedAt = time.Now()
	return nil
}

func (m *MemoryStore) FailState(ctx context.Context, execID, stateName string, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := stateKey(execID, stateName)
	rec, ok := m.states[key]
	if !ok {
		return ErrStateNotFound
	}
	rec.Status = StatusFailed
	rec.Error = errMsg
	rec.UpdatedAt = time.Now()
	return nil
}

func (m *MemoryStore) GetStateOutput(ctx context.Context, execID, stateName string) (kflow.Output, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := stateKey(execID, stateName)
	rec, ok := m.states[key]
	if !ok {
		return nil, ErrStateNotFound
	}
	if rec.Status != StatusCompleted {
		return nil, ErrStateNotCompleted
	}
	// return a copy
	out := make(kflow.Output, len(rec.Output))
	for k, v := range rec.Output {
		out[k] = v
	}
	return out, nil
}
