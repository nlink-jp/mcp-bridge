package oauth

import (
	"net/http"
	"net/url"
)

// applyClientAuth puts client credentials where the token endpoint expects
// them, per the profile's clientAuthMethod.
//
//   - "post" (default): client_id and client_secret in the form body. The
//     most widely accepted method.
//   - "basic": HTTP Basic authentication. Required by Microsoft Entra ID and
//     some Okta tenants. RFC 6749 §2.3.1 forbids sending the credentials both
//     ways, so the form is left alone here.
//   - "none": a public client using PKCE only. client_id in the form, never a
//     secret; config validation rejects a secret in this mode.
//
// The method stays explicit rather than being read from the authorization
// server's token_endpoint_auth_methods_supported: providers without dynamic
// registration frequently publish that field incorrectly, and a wrong guess
// produces an opaque 401 at the token endpoint.
//
// Call this before encoding form into the request body.
func applyClientAuth(req *http.Request, form url.Values, method, clientID, clientSecret string) {
	switch method {
	case "basic":
		req.SetBasicAuth(clientID, clientSecret)
	case "none":
		form.Set("client_id", clientID)
	default: // "" or "post"
		form.Set("client_id", clientID)
		if clientSecret != "" {
			form.Set("client_secret", clientSecret)
		}
	}
}
