package runservice

import (
	"encoding/json"
	"net/http"
	"strings"
)

func NewHandler(service *Service, verifier AuthVerifier) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("/api/runs", RequireAuth(verifier, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		user, ok := UserFromContext(r.Context())
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated user missing"})
			return
		}

		var request CreateRunRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		run, err := service.CreateRun(r.Context(), user, request)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, run)
	})))
	mux.Handle("/api/runs/", RequireAuth(verifier, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		user, ok := UserFromContext(r.Context())
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated user missing"})
			return
		}

		runID, pivotID, err := parseDecisionPath(r.URL.Path)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}

		var request struct {
			Decision PivotDecisionInput `json:"decision"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		pivot, err := service.DecidePivot(r.Context(), user, runID, pivotID, request.Decision)
		if err != nil {
			writeJSON(w, statusForError(err), map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, pivot)
	})))
	return recoverJSON(withAPICORS(mux))
}

func parseDecisionPath(path string) (runID string, pivotID string, err error) {
	trimmed := strings.Trim(path, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 6 {
		return "", "", newNotFoundError("route not found")
	}
	if parts[0] != "api" || parts[1] != "runs" || parts[3] != "pivots" || parts[5] != "decision" {
		return "", "", newNotFoundError("route not found")
	}
	if parts[2] == "" || parts[4] == "" {
		return "", "", newNotFoundError("route not found")
	}
	return parts[2], parts[4], nil
}

var allowedCORSOrigins = map[string]struct{}{
	"http://localhost:5173":                             {},
	"http://127.0.0.1:5173":                             {},
	"https://asset-discovery-0325-f111.web.app":         {},
	"https://asset-discovery-0325-f111.firebaseapp.com": {},
}

func withAPICORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}

		if _, ok := allowedCORSOrigins[origin]; !ok {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin is not allowed"})
			return
		}

		headers := w.Header()
		headers.Set("Access-Control-Allow-Origin", origin)
		headers.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		headers.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		headers.Add("Vary", "Origin")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
