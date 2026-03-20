package api

import (
	"encoding/json"
	"net/http"
	"regexp"
)

var workflowNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

type retryPolicy struct {
	MaxAttempts       int `json:"max_attempts"`
	BackoffSeconds    int `json:"backoff_seconds"`
	MaxBackoffSeconds int `json:"max_backoff_seconds"`
}

type stateNode struct {
	Name          string       `json:"name"`
	Type          string       `json:"type"`
	HandlerRef    string       `json:"handler_ref"`
	ServiceTarget string       `json:"service_target"`
	Catch         string       `json:"catch"`
	Retry         *retryPolicy `json:"retry,omitempty"`
}

type flowEntry struct {
	Name  string       `json:"name"`
	Next  string       `json:"next"`
	Catch string       `json:"catch"`
	IsEnd bool         `json:"is_end"`
	Retry *retryPolicy `json:"retry,omitempty"`
}

type workflowGraph struct {
	Name   string      `json:"name"`
	States []stateNode `json:"states"`
	Flow   []flowEntry `json:"flow"`
}

func (s *Server) handleRegisterWorkflow(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var g workflowGraph
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body", "invalid_graph")
		return
	}

	if !workflowNameRe.MatchString(g.Name) {
		writeError(w, http.StatusBadRequest, "name must match ^[a-z0-9-]+$", "invalid_graph")
		return
	}
	if len(g.States) == 0 {
		writeError(w, http.StatusBadRequest, "states must not be empty", "invalid_graph")
		return
	}
	if len(g.Flow) == 0 {
		writeError(w, http.StatusBadRequest, "flow must not be empty", "invalid_graph")
		return
	}

	// Check state names are unique and build lookup set.
	stateSet := make(map[string]struct{}, len(g.States))
	for _, st := range g.States {
		if _, dup := stateSet[st.Name]; dup {
			writeError(w, http.StatusBadRequest, "duplicate state name: "+st.Name, "invalid_graph")
			return
		}
		stateSet[st.Name] = struct{}{}
	}

	// Validate flow entries reference known states.
	for _, fe := range g.Flow {
		if _, ok := stateSet[fe.Name]; !ok {
			writeError(w, http.StatusBadRequest, "flow references unknown state: "+fe.Name, "invalid_graph")
			return
		}
	}

	s.graphsMu.Lock()
	defer s.graphsMu.Unlock()

	if _, exists := s.graphs[g.Name]; exists {
		writeError(w, http.StatusConflict, "workflow already registered", "conflict")
		return
	}
	s.graphs[g.Name] = g
	writeJSON(w, http.StatusCreated, map[string]string{"name": g.Name})
}

func (s *Server) handleGetWorkflow(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	s.graphsMu.RLock()
	g, ok := s.graphs[name]
	s.graphsMu.RUnlock()

	if !ok {
		writeError(w, http.StatusNotFound, "workflow not found", "not_found")
		return
	}
	writeJSON(w, http.StatusOK, g)
}
