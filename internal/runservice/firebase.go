package runservice

import (
	"context"
	"fmt"
	"strings"

	firebase "firebase.google.com/go/v4"
	firebaseauth "firebase.google.com/go/v4/auth"
	"google.golang.org/api/option"
)

type FirebaseVerifier struct {
	client *firebaseauth.Client
}

func NewFirebaseVerifier(ctx context.Context, options ...option.ClientOption) (*FirebaseVerifier, error) {
	app, err := firebase.NewApp(ctx, nil, options...)
	if err != nil {
		return nil, fmt.Errorf("create firebase app: %w", err)
	}
	client, err := app.Auth(ctx)
	if err != nil {
		return nil, fmt.Errorf("create firebase auth client: %w", err)
	}
	return &FirebaseVerifier{client: client}, nil
}

func NewFirebaseVerifierFromApp(ctx context.Context, app *firebase.App) (*FirebaseVerifier, error) {
	client, err := app.Auth(ctx)
	if err != nil {
		return nil, fmt.Errorf("create firebase auth client: %w", err)
	}
	return &FirebaseVerifier{client: client}, nil
}

func (v *FirebaseVerifier) VerifyIDToken(ctx context.Context, token string) (AuthenticatedUser, error) {
	verified, err := v.client.VerifyIDToken(ctx, strings.TrimSpace(token))
	if err != nil {
		return AuthenticatedUser{}, fmt.Errorf("verify id token: %w", err)
	}

	user := AuthenticatedUser{
		UID: verified.UID,
	}
	if email, ok := verified.Claims["email"].(string); ok {
		user.Email = email
	}
	if verifiedEmail, ok := verified.Claims["email_verified"].(bool); ok {
		user.EmailVerified = verifiedEmail
	}
	if name, ok := verified.Claims["name"].(string); ok {
		user.Name = name
	}

	return user, nil
}
