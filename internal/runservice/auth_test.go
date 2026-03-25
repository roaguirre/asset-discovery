package runservice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubVerifier struct {
	user AuthenticatedUser
	err  error
}

func (s stubVerifier) VerifyIDToken(_ context.Context, _ string) (AuthenticatedUser, error) {
	return s.user, s.err
}

func TestIsAllowedEmail(t *testing.T) {
	t.Parallel()

	cases := []struct {
		email string
		want  bool
	}{
		{email: "analyst@zerofox.com", want: true},
		{email: "roaguirred@gmail.com", want: true},
		{email: "outsider@example.com", want: false},
	}

	for _, testCase := range cases {
		if got := IsAllowedEmail(testCase.email); got != testCase.want {
			t.Fatalf("IsAllowedEmail(%q) = %v, want %v", testCase.email, got, testCase.want)
		}
	}
}

func TestRequireAuthRejectsUnverifiedOrDisallowedUsers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		user     AuthenticatedUser
		wantCode int
	}{
		{
			name: "verified allowed",
			user: AuthenticatedUser{UID: "uid-1", Email: "reviewer@zerofox.com", EmailVerified: true},
			wantCode: http.StatusNoContent,
		},
		{
			name: "unverified",
			user: AuthenticatedUser{UID: "uid-2", Email: "reviewer@zerofox.com", EmailVerified: false},
			wantCode: http.StatusForbidden,
		},
		{
			name: "disallowed",
			user: AuthenticatedUser{UID: "uid-3", Email: "reviewer@example.com", EmailVerified: true},
			wantCode: http.StatusForbidden,
		},
	}

	for _, testCase := range tests {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/runs", nil)
		request.Header.Set("Authorization", "Bearer token")

		handler := RequireAuth(stubVerifier{user: testCase.user}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if user, ok := UserFromContext(r.Context()); !ok || user.UID != testCase.user.UID {
				t.Fatalf("expected authenticated user to be attached to the request context")
			}
			w.WriteHeader(http.StatusNoContent)
		}))

		handler.ServeHTTP(recorder, request)

		if recorder.Code != testCase.wantCode {
			t.Fatalf("%s: expected status %d, got %d", testCase.name, testCase.wantCode, recorder.Code)
		}
	}
}
