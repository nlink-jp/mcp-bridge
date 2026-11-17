package cli

import (
	"reflect"
	"testing"

	"github.com/nlink-jp/mcp-bridge/internal/config"
)

// A key that validates and is then dropped on the way to the login is a
// feature that never happens, with nothing on screen to say so. Every field of
// the config block has to arrive.
func TestEveryOAuthSettingReachesTheLogin(t *testing.T) {
	block := &config.OAuth{
		AuthorizeURL:     "https://a.example.com/authorize",
		TokenURL:         "https://a.example.com/token",
		ClientID:         "id",
		ClientSecret:     "secret",
		Scopes:           []string{"read"},
		ExtraParams:      map[string]string{"k": "v"},
		CallbackPort:     7777,
		CallbackScheme:   "https",
		CallbackHost:     "localhost",
		ClientAuthMethod: "basic",
	}
	// The fixture itself must set every field, or a new one would pass unseen.
	in := reflect.ValueOf(*block)
	for i := 0; i < in.NumField(); i++ {
		if in.Field(i).IsZero() {
			t.Fatalf("the fixture leaves config.OAuth.%s unset", in.Type().Field(i).Name)
		}
	}

	out := reflect.ValueOf(oauthSettings(&config.Server{OAuth: block}))
	for i := 0; i < out.NumField(); i++ {
		if out.Field(i).IsZero() {
			t.Errorf("oauth.Settings.%s was not filled from the config", out.Type().Field(i).Name)
		}
	}
	if in.NumField() != out.NumField() {
		t.Errorf("config.OAuth has %d fields and oauth.Settings has %d: one of them is not carried", in.NumField(), out.NumField())
	}
}
