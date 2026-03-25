package runservice

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandler_CORSPreflightAllowedOrigin(t *testing.T) {
	t.Parallel()

	handler := NewHandler(nil, nil)
	request := httptest.NewRequest(http.MethodOptions, "/api/runs", nil)
	request.Header.Set("Origin", "http://localhost:5173")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, recorder.Code)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("expected allowed origin header, got %q", got)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Headers"); got != "Authorization, Content-Type" {
		t.Fatalf("expected allow headers, got %q", got)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Methods"); got != "POST, OPTIONS" {
		t.Fatalf("expected allow methods, got %q", got)
	}
}

func TestHandler_CORSRejectsDisallowedOrigin(t *testing.T) {
	t.Parallel()

	handler := NewHandler(nil, nil)
	request := httptest.NewRequest(http.MethodOptions, "/api/runs", nil)
	request.Header.Set("Origin", "https://example.com")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, recorder.Code)
	}
}

func TestHandler_CORSHeadersOnAllowedOriginRequest(t *testing.T) {
	t.Parallel()

	handler := NewHandler(nil, stubVerifier{})
	request := httptest.NewRequest(http.MethodPost, "/api/runs", nil)
	request.Header.Set("Origin", "https://asset-discovery-0325-f111.web.app")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, recorder.Code)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "https://asset-discovery-0325-f111.web.app" {
		t.Fatalf("expected allowed origin header, got %q", got)
	}
}

func TestHandler_NoOriginRequestSkipsCORS(t *testing.T) {
	t.Parallel()

	handler := NewHandler(nil, stubVerifier{})
	request := httptest.NewRequest(http.MethodPost, "/api/runs", nil)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, recorder.Code)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no CORS header, got %q", got)
	}
}

func TestHandler_DecidePivotMapsErrorStatuses(t *testing.T) {
	t.Parallel()

	service, _, projection, run := newTestService(t, RunModeManual, &capturingArtifactStore{})
	if err := service.ProcessRun(context.Background(), run.ID); err != nil {
		t.Fatalf("ProcessRun() error = %v", err)
	}

	var pivotID string
	for _, pivot := range projection.Pivots[run.ID] {
		pivotID = pivot.ID
	}
	if pivotID == "" {
		t.Fatal("expected pending pivot")
	}

	t.Run("forbidden", func(t *testing.T) {
		handler := NewHandler(service, stubVerifier{
			user: AuthenticatedUser{
				UID:           "uid-2",
				Email:         "other@zerofox.com",
				EmailVerified: true,
			},
		})

		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/runs/"+run.ID+"/pivots/"+pivotID+"/decision", bytes.NewBufferString(`{"decision":"accepted"}`))
		request.Header.Set("Authorization", "Bearer token")

		handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusForbidden {
			t.Fatalf("expected status %d, got %d", http.StatusForbidden, recorder.Code)
		}
	})

	t.Run("not found", func(t *testing.T) {
		handler := NewHandler(service, stubVerifier{
			user: AuthenticatedUser{
				UID:           "uid-1",
				Email:         "reviewer@zerofox.com",
				EmailVerified: true,
			},
		})

		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/runs/"+run.ID+"/pivots/missing/decision", bytes.NewBufferString(`{"decision":"accepted"}`))
		request.Header.Set("Authorization", "Bearer token")

		handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusNotFound {
			t.Fatalf("expected status %d, got %d", http.StatusNotFound, recorder.Code)
		}
	})

	t.Run("bad request", func(t *testing.T) {
		handler := NewHandler(service, stubVerifier{
			user: AuthenticatedUser{
				UID:           "uid-1",
				Email:         "reviewer@zerofox.com",
				EmailVerified: true,
			},
		})

		firstRequest := httptest.NewRequest(http.MethodPost, "/api/runs/"+run.ID+"/pivots/"+pivotID+"/decision", bytes.NewBufferString(`{"decision":"accepted"}`))
		firstRequest.Header.Set("Authorization", "Bearer token")
		firstRecorder := httptest.NewRecorder()
		handler.ServeHTTP(firstRecorder, firstRequest)
		if firstRecorder.Code != http.StatusOK {
			t.Fatalf("expected first decision status %d, got %d", http.StatusOK, firstRecorder.Code)
		}

		secondRequest := httptest.NewRequest(http.MethodPost, "/api/runs/"+run.ID+"/pivots/"+pivotID+"/decision", bytes.NewBufferString(`{"decision":"accepted"}`))
		secondRequest.Header.Set("Authorization", "Bearer token")
		secondRecorder := httptest.NewRecorder()
		handler.ServeHTTP(secondRecorder, secondRequest)

		if secondRecorder.Code != http.StatusBadRequest {
			t.Fatalf("expected status %d, got %d", http.StatusBadRequest, secondRecorder.Code)
		}
	})
}

func TestStatusForError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want int
	}{
		{name: "forbidden", err: newForbiddenError("forbidden"), want: http.StatusForbidden},
		{name: "not found", err: newNotFoundError("missing"), want: http.StatusNotFound},
		{name: "bad request", err: context.Canceled, want: http.StatusBadRequest},
	}

	for _, testCase := range cases {
		if got := statusForError(testCase.err); got != testCase.want {
			t.Fatalf("%s: expected status %d, got %d", testCase.name, testCase.want, got)
		}
	}
}

func decodeErrorBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]string {
	t.Helper()

	var payload map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	return payload
}
