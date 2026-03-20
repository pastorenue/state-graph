package api

import (
	"encoding/json"
	"net/http"

	"github.com/pastorenue/kflow/internal/store"
)

func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	svcs, err := s.Store.ListServices(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list services", "internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": svcs})
}

func (s *Server) handleCreateService(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	var rec store.ServiceRecord
	if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "bad_request")
		return
	}
	if rec.Name == "" {
		writeError(w, http.StatusBadRequest, "service name is required", "bad_request")
		return
	}

	// Name collision check: reject if already registered and not Stopped.
	existing, err := s.Store.GetService(r.Context(), rec.Name)
	if err == nil && existing.Status != store.ServiceStatusStopped {
		writeError(w, http.StatusConflict, "service name already registered", "name_collision")
		return
	}

	if err := s.Store.CreateService(r.Context(), rec); err == store.ErrDuplicateServiceName {
		writeError(w, http.StatusConflict, "service name already registered", "name_collision")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create service", "internal")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"name": rec.Name})
}

func (s *Server) handleGetService(w http.ResponseWriter, r *http.Request) {
	rec, err := s.Store.GetService(r.Context(), r.PathValue("name"))
	if err == store.ErrServiceNotFound {
		writeError(w, http.StatusNotFound, "service not found", "not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get service", "internal")
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	rec, err := s.Store.GetService(r.Context(), name)
	if err == store.ErrServiceNotFound {
		writeError(w, http.StatusNotFound, "service not found", "not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get service", "internal")
		return
	}

	// Tear down K8s resources for Deployment-mode services.
	if s.K8s != nil {
		svcName := rec.Name
		_ = s.K8s.DeleteDeployment(r.Context(), svcName)
		if rec.IngressHost != "" {
			_ = s.K8s.DeleteIngress(r.Context(), svcName)
		}
	}

	if err := s.Store.DeleteService(r.Context(), name); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete service", "internal")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
