package zerops

import "testing"

// The consent page URL is the API's unless a GUI origin stands in for its scheme and host; the request stays.
func TestConsentUrl(t *testing.T) {
	const verification = "https://futon.devel.zerops.dev/authorize-login?request=abc"
	if got, err := consentUrl(verification, ""); err != nil || got != verification {
		t.Error(got, err)
	}
	if got, err := consentUrl(verification, "http://localhost:1111"); err != nil || got != "http://localhost:1111/authorize-login?request=abc" {
		t.Error(got, err)
	}
	if _, err := consentUrl(verification, "localhost"); err == nil {
		t.Error("an origin without a scheme and host passed")
	}
}
