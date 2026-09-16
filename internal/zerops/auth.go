package zerops

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/zeropsio/zerops-go/dto/input/body"
	"github.com/zeropsio/zerops-go/dto/output"
	"github.com/zeropsio/zerops-go/sdk"
	"github.com/zeropsio/zerops-go/sdkBase"
	"github.com/zeropsio/zerops-go/types"
)

// Session is the short-lived login the API answers email and password with. The CLI does not keep it: it mints a personal access token from it and stores that.
type Session struct {
	sdk sdk.Handler
}

func unauthenticated(apiUrl string) sdk.Handler {
	return sdk.New(sdkBase.DefaultConfig(sdkBase.WithCustomEndpoint(apiUrl)), &http.Client{Timeout: 60 * time.Second})
}

// Login opens a session with email and password. An account with two-factor authentication turned on cannot finish here, the CLI has no second factor: the personal access token from the Zerops GUI is the way in for it.
func Login(ctx context.Context, apiUrl, email, password string) (*Session, error) {
	resp, err := unauthenticated(apiUrl).PostAuthLogin(ctx, body.Auth{Email: types.NewEmail(email), Password: types.NewString(password)})
	if err != nil {
		return nil, err
	}
	out, err := resp.Output()
	if err != nil {
		return nil, err
	}
	return sessionFrom(apiUrl, out.Auth)
}

type Registration struct {
	Email       string
	Password    string
	Name        string
	AccountName string
}

// Register creates the account and its organisation and opens the session of the new user.
func Register(ctx context.Context, apiUrl string, r Registration) (*Session, error) {
	resp, err := unauthenticated(apiUrl).PostRegistration(ctx, body.UserRegistration{
		Email:       types.NewEmail(r.Email),
		Password:    types.NewStringNull(r.Password),
		Name:        types.NewString(r.Name),
		AccountName: types.NewString(r.AccountName),
	})
	if err != nil {
		return nil, err
	}
	out, err := resp.Output()
	if err != nil {
		return nil, err
	}
	return sessionFrom(apiUrl, out.Auth)
}

func sessionFrom(apiUrl string, auth output.AuthFull) (*Session, error) {
	if methods, ok := auth.TwoFAMethods.Get(); ok && len(methods) > 0 {
		if verified, ok := auth.TwoFAVerified.Get(); !ok || !verified.Native() {
			return nil, errors.New("the account uses two-factor authentication, which the CLI cannot answer: create a personal access token in the Zerops GUI and run `zbox login --token <token>`")
		}
	}
	token := auth.AccessToken.Native()
	if token == "" {
		return nil, errors.New("the API answered without a session token")
	}
	return &Session{sdk: sdk.AuthorizeSdk(unauthenticated(apiUrl), token)}, nil
}

// MintToken creates a personal access token, the long-lived credential the CLI stores. It is listed under the name in the Zerops GUI, where it can be revoked.
func (s *Session) MintToken(ctx context.Context, name string) (string, error) {
	resp, err := s.sdk.PostUserToken(ctx, body.UserToken{Name: types.NewString(name)})
	if err != nil {
		return "", err
	}
	out, err := resp.Output()
	if err != nil {
		return "", err
	}
	return out.Token.Native(), nil
}
