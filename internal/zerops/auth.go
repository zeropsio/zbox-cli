package zerops

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/zeropsio/zerops-go/apiError"
	"github.com/zeropsio/zerops-go/dto/input/body"
	"github.com/zeropsio/zerops-go/dto/output"
	"github.com/zeropsio/zerops-go/errorCode"
	"github.com/zeropsio/zerops-go/sdk"
	"github.com/zeropsio/zerops-go/sdkBase"
	"github.com/zeropsio/zerops-go/types"
	"github.com/zeropsio/zerops-go/types/enum"

	"github.com/zeropsio/zbox-cli/internal/browser"
)

// appCode is the row of the public API's loginApp table that names zbox on the consent page, a PUBLIC app without a secret.
const appCode = "zbox"

// Session is what a login answers and the CLI stores: the access token every public API call sends, when it runs out,
// and the refresh token that gets the next pair. The CLI mints no personal access token.
type Session struct {
	AccessToken  string
	ExpiresAt    time.Time
	RefreshToken string
}

func unauthenticated(apiUrl string) sdk.Handler {
	return sdk.New(sdkBase.DefaultConfig(sdkBase.WithCustomEndpoint(apiUrl)), &http.Client{Timeout: 60 * time.Second})
}

// Login opens a session with email and password. An account with two-factor authentication turned on cannot finish here, the CLI has no second factor: the browser login is the way in for it.
func Login(ctx context.Context, apiUrl, email, password string) (Session, error) {
	resp, err := unauthenticated(apiUrl).PostAuthLogin(ctx, body.Auth{Email: types.NewEmail(email), Password: types.NewString(password)})
	if err != nil {
		return Session{}, err
	}
	out, err := resp.Output()
	if err != nil {
		return Session{}, err
	}
	return sessionFrom(out.Auth)
}

type Registration struct {
	Email       string
	Password    string
	Name        string
	AccountName string
}

// Register creates the account and its organisation and opens the session of the new user.
func Register(ctx context.Context, apiUrl string, r Registration) (Session, error) {
	resp, err := unauthenticated(apiUrl).PostRegistration(ctx, body.UserRegistration{
		Email:       types.NewEmail(r.Email),
		Password:    types.NewStringNull(r.Password),
		Name:        types.NewString(r.Name),
		AccountName: types.NewString(r.AccountName),
	})
	if err != nil {
		return Session{}, err
	}
	out, err := resp.Output()
	if err != nil {
		return Session{}, err
	}
	return sessionFrom(out.Auth)
}

// Refresh trades the refresh token for a new pair. The old pair is dead afterwards, so the caller stores the answer at once.
func Refresh(ctx context.Context, apiUrl, refreshToken string) (Session, error) {
	resp, err := unauthenticated(apiUrl).PostAuthRefresh(ctx, body.AuthRefresh{RefreshTokenId: types.NewString(refreshToken)})
	if err != nil {
		return Session{}, err
	}
	out, err := resp.Output()
	if err != nil {
		return Session{}, err
	}
	return sessionFrom(out)
}

// DeviceLogin is the browser login: the API opens a login request for zbox, the user approves it on the consent page of the Zerops GUI
// (signing in or registering there first) and the CLI, which polls the request, receives the session. Nothing listens on this machine.
// guiUrl replaces the origin of the consent page the API names, for a GUI other than the environment's deployed one; empty keeps the API's.
func DeviceLogin(ctx context.Context, apiUrl, guiUrl string, print func(string)) (Session, error) {
	h := unauthenticated(apiUrl)
	resp, err := h.PostAuthDeviceLogin(ctx, body.DeviceLoginInitiate{AppCode: types.NewString(appCode)})
	if err != nil {
		return Session{}, err
	}
	request, err := resp.Output()
	if err != nil {
		return Session{}, err
	}
	login, err := approve(ctx, h, request, guiUrl, print)
	if err != nil {
		return Session{}, err
	}
	if login == nil {
		return Session{}, errors.New("the API approved the login without a session")
	}
	return sessionFrom(login.Auth)
}

// Sudo puts the session into sudo mode, which reading a secret of the account needs, through the same consent page as the login.
// The API refuses it for a personal access token.
func (c *Client) Sudo(ctx context.Context, guiUrl string, print func(string)) error {
	resp, err := c.sdk.PostAuthDeviceLoginSudo(ctx, body.DeviceLoginInitiate{AppCode: types.NewString(appCode)})
	if err != nil {
		return err
	}
	request, err := resp.Output()
	if err != nil {
		return err
	}
	_, err = approve(ctx, c.sdk, request, guiUrl, print)
	return err
}

func (c *Client) InSudo(ctx context.Context) (bool, error) {
	resp, err := c.sdk.GetAuthSudo(ctx)
	if err != nil {
		return false, err
	}
	out, err := resp.Output()
	if err != nil {
		return false, err
	}
	return out.InSudo.Native(), nil
}

// Logout ends the session on the API, so the stored refresh token cannot be used after `zbox logout`.
func (c *Client) Logout(ctx context.Context) error {
	_, err := c.sdk.PostAuthLogout(ctx)
	return err
}

// approve opens the consent page and polls the request until the user decides or it expires. A LOGIN request answers the session, a SUDO request nil.
func approve(ctx context.Context, h sdk.Handler, request output.DeviceLoginInitiate, guiUrl string, print func(string)) (*output.AuthLogin, error) {
	target, err := consentUrl(request.VerificationUrl.Native(), guiUrl)
	if err != nil {
		return nil, err
	}
	print(fmt.Sprintf("Confirm code %s in the browser: %s", request.UserCode.Native(), target))
	if err := browser.Open(target); err != nil {
		print("Could not open a browser, open the URL above.")
	}
	interval := time.Duration(max(request.Interval.Native(), 1)) * time.Second
	deadline := time.Now().Add(time.Duration(request.ExpiresIn.Native()) * time.Second)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		resp, err := h.PostAuthDeviceLoginCheck(ctx, body.DeviceLoginCheck{RequestId: request.RequestId, PollSecret: request.PollSecret})
		if err != nil {
			// The request is removed when it expires and after a denial was collected.
			if apiError.HasErrorCode(err, errorCode.DeviceLoginNotFound) {
				return nil, errors.New("the login request expired, run the command again")
			}
			return nil, err
		}
		out, err := resp.Output()
		if err != nil {
			return nil, err
		}
		switch out.Status {
		case enum.DeviceLoginStatusEnumApproved:
			return out.Auth, nil
		case enum.DeviceLoginStatusEnumDenied:
			return nil, errors.New("denied in the browser")
		}
		if time.Now().After(deadline) {
			return nil, errors.New("the login request expired, run the command again")
		}
	}
}

// consentUrl keeps the API's verification URL unless a GUI origin is given, which then stands in for its scheme and host.
func consentUrl(verificationUrl, guiUrl string) (string, error) {
	if guiUrl == "" {
		return verificationUrl, nil
	}
	target, err := url.Parse(verificationUrl)
	if err != nil {
		return "", fmt.Errorf("the API answered a verification URL that does not parse: %w", err)
	}
	gui, err := url.Parse(guiUrl)
	if err != nil || gui.Host == "" {
		return "", fmt.Errorf("the GUI URL %q is not an origin", guiUrl)
	}
	target.Scheme = gui.Scheme
	target.Host = gui.Host
	return target.String(), nil
}

func sessionFrom(auth output.AuthFull) (Session, error) {
	if methods, ok := auth.TwoFAMethods.Get(); ok && len(methods) > 0 {
		if verified, ok := auth.TwoFAVerified.Get(); !ok || !verified.Native() {
			return Session{}, errors.New("the account uses two-factor authentication, which the terminal login cannot answer: run `zbox login` without --email")
		}
	}
	refresh, _ := auth.RefreshToken.Get()
	if auth.AccessToken.Native() == "" || refresh.Native() == "" {
		return Session{}, errors.New("the API answered without a session")
	}
	return Session{AccessToken: auth.AccessToken.Native(), ExpiresAt: auth.ExpiresAt.Native(), RefreshToken: refresh.Native()}, nil
}
