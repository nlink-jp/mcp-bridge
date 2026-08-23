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
//
// A missing OAuth login is an error here. run uses this: an MCP client that
// launches the bridge cannot surface a per-request failure as legibly as a
// terminal can, so refusing to start with the login command named is kinder
// than a session where every call fails.
func transportOptions(srv *config.Server, name string, logs io.Writer) ([]transport.Option, error) {
	opts, _, err := buildTransportOptions(srv, name, logs, false)
	return opts, err
}

// credential describes what the connection will present, for reporting.
type credential struct {
	mode config.AuthMode
	// presented is false when nothing will be sent: either none is configured,
	// or an OAuth login is missing and the caller allowed that.
	presented bool
	// why explains a false presented, in words meant for a user.
	why string
}

// buildTransportOptions assembles the transport options and reports what will
// be presented.
//
// When allowMissingLogin is set, a server with no stored login yields a
// transport that sends no credential rather than an error. inspect uses this:
// its whole job is answering "is this configuration right?", and before the
// first login is exactly when that gets asked. Many MCP servers answer
// initialize and tools/list unauthenticated — the Google Workspace servers do —
// so there is real information to be had without a token.
func buildTransportOptions(srv *config.Server, name string, logs io.Writer, allowMissingLogin bool) ([]transport.Option, credential, error) {
	mode := srv.AuthMode()
	switch mode {
	case config.AuthNone:
		return nil, credential{mode: mode, why: "no credential is configured"}, nil

	case config.AuthStaticHeaders:
		return []transport.Option{transport.WithHeaders(srv.Headers)},
			credential{mode: mode, presented: true}, nil

	case config.AuthTokenCommand:
		provider := tokencmd.New(srv.TokenCommand.Command, srv.TokenCommand.Args, 0)
		return []transport.Option{
				transport.WithTokenProvider(provider),
				transport.WithHeaders(srv.Headers),
			},
			credential{mode: mode, presented: true}, nil

	case config.AuthOAuthConfigured, config.AuthOAuthDiscover:
		provider, err := oauthProvider(srv, name, logs)
		if err != nil {
			if !allowMissingLogin {
				return nil, credential{mode: mode}, err
			}
			var opts []transport.Option
			if len(srv.Headers) > 0 {
				opts = append(opts, transport.WithHeaders(srv.Headers))
			}
			return opts, credential{mode: mode, why: "not logged in"}, nil
		}
		return []transport.Option{
				transport.WithTokenProvider(provider),
				transport.WithHeaders(srv.Headers),
			},
			credential{mode: mode, presented: true}, nil

	default:
		return nil, credential{mode: mode}, fmt.Errorf("server %q: unknown authentication mode", name)
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
