package cli

import (
	"fmt"
	"io"

	"github.com/nlink-jp/mcp-bridge/internal/config"
	"github.com/nlink-jp/mcp-bridge/internal/oauth"
	"github.com/nlink-jp/mcp-bridge/internal/tokencmd"
	"github.com/nlink-jp/mcp-bridge/internal/transport"
)

// transportOptions turns a server's authentication settings into the options
// the transport needs to authenticate every request.
func transportOptions(srv *config.Server, name string, logs io.Writer) ([]transport.Option, error) {
	switch srv.AuthMode() {
	case config.AuthNone:
		return nil, nil

	case config.AuthStaticHeaders:
		return []transport.Option{transport.WithHeaders(srv.Headers)}, nil

	case config.AuthTokenCommand:
		provider := tokencmd.New(srv.TokenCommand.Command, srv.TokenCommand.Args, 0)
		return []transport.Option{
			transport.WithTokenProvider(provider),
			transport.WithHeaders(srv.Headers),
		}, nil

	case config.AuthOAuthConfigured, config.AuthOAuthDiscover:
		provider, err := oauthProvider(srv, name, logs)
		if err != nil {
			return nil, err
		}
		return []transport.Option{
			transport.WithTokenProvider(provider),
			transport.WithHeaders(srv.Headers),
		}, nil

	default:
		return nil, fmt.Errorf("server %q: unknown authentication mode", name)
	}
}

// oauthProvider builds a token provider over the stored login.
//
// A server configured with an empty oauth block has no endpoints in the config
// file; they were recorded by the login, so they come from the discovery
// cache. Losing that cache is recoverable — log in again — and the error says
// so rather than reporting a missing file.
func oauthProvider(srv *config.Server, name string, logs io.Writer) (*oauth.Provider, error) {
	tokensPath, err := config.TokensPath(name)
	if err != nil {
		return nil, err
	}

	cfg := oauth.ProviderConfig{
		ServerName: name,
		TokensPath: tokensPath,
		Logs:       logs,
	}
	if o := srv.OAuth; o != nil {
		cfg.TokenURL = o.TokenURL
		cfg.ClientID = o.ClientID
		cfg.ClientSecret = o.ClientSecret
		cfg.ClientAuthMethod = o.ClientAuthMethod
	}

	if cfg.TokenURL == "" || cfg.ClientID == "" {
		discoveryPath, err := config.DiscoveryPath(name)
		if err != nil {
			return nil, err
		}
		discovered, ok := oauth.LoadDiscovered(discoveryPath)
		if !ok {
			return nil, fmt.Errorf("server %q has no OAuth endpoints configured and nothing was discovered yet: run \"mcp-bridge login %s\"", name, name)
		}
		if cfg.TokenURL == "" {
			cfg.TokenURL = discovered.TokenURL
		}
		if cfg.ClientID == "" {
			cfg.ClientID = discovered.ClientID
		}
		if cfg.ClientSecret == "" {
			cfg.ClientSecret = discovered.ClientSecret
		}
		// A dynamically registered client is public, so it has no secret to
		// present at the token endpoint.
		if cfg.ClientAuthMethod == "" && cfg.ClientSecret == "" {
			cfg.ClientAuthMethod = "none"
		}
	}

	return oauth.NewProvider(cfg)
}

// oauthSettings maps the config block onto the login flow's own settings.
func oauthSettings(srv *config.Server) oauth.Settings {
	o := srv.OAuth
	if o == nil {
		return oauth.Settings{}
	}
	return oauth.Settings{
		AuthorizeURL:     o.AuthorizeURL,
		TokenURL:         o.TokenURL,
		ClientID:         o.ClientID,
		ClientSecret:     o.ClientSecret,
		Scopes:           o.Scopes,
		ExtraParams:      o.ExtraParams,
		CallbackPort:     o.CallbackPort,
		CallbackScheme:   o.CallbackScheme,
		ClientAuthMethod: o.ClientAuthMethod,
	}
}
