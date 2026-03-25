package runservice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type userContextKey struct{}

func UserFromContext(ctx context.Context) (AuthenticatedUser, bool) {
	user, ok := ctx.Value(userContextKey{}).(AuthenticatedUser)
	return user, ok
}

func IsAllowedEmail(email string) bool {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return false
	}
	if email == "roaguirred@gmail.com" {
		return true
	}
	return strings.HasSuffix(email, "@zerofox.com")
}

func RequireAuth(verifier AuthVerifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := authenticateRequest(r.Context(), verifier, r.Header.Get("Authorization"))
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}
		if !user.EmailVerified {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "verified email is required"})
			return
		}
		if !IsAllowedEmail(user.Email) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "email is not allowlisted"})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, user)))
	})
}

func authenticateRequest(ctx context.Context, verifier AuthVerifier, authorization string) (AuthenticatedUser, error) {
	if verifier == nil {
		return AuthenticatedUser{}, errors.New("auth verifier is not configured")
	}
	token := strings.TrimSpace(authorization)
	if token == "" {
		return AuthenticatedUser{}, errors.New("authorization header is required")
	}
	if !strings.HasPrefix(strings.ToLower(token), "bearer ") {
		return AuthenticatedUser{}, errors.New("authorization header must use bearer token")
	}
	return verifier.VerifyIDToken(ctx, strings.TrimSpace(token[len("Bearer "):]))
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
