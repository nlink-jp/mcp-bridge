package cli

import (
	"fmt"
	"io"

	"github.com/nlink-jp/mcp-bridge/internal/config"
	"github.com/nlink-jp/mcp-bridge/internal/oauth"
)

// LoginOptions describes one interactive login.
type LoginOptions struct {
	ConfigPath   string
	Server       string
	CallbackPort int // overrides the configured port; 0 leaves it alone
	Out          io.Writer
}

// Login runs the OAuth flow for a server and stores the resulting tokens.
func Login(opts LoginOptions) error {
	srv, err := loadServer(opts.ConfigPath, opts.Server)
	if err != nil {
		return err
	}

	switch srv.AuthMode() {
	case config.AuthOAuthConfigured, config.AuthOAuthDiscover:
	default:
		// Nothing to log in to, and silently succeeding would leave the user
		// waiting for a browser that never opens.
		return fmt.Errorf("server %q does not use OAuth (authentication is %q), so there is nothing to log in to",
			opts.Server, srv.AuthMode())
	}

	settings := oauthSettings(srv)
	if opts.CallbackPort != 0 {
		settings.CallbackPort = opts.CallbackPort
	}

	tokensPath, err := config.TokensPath(opts.Server)
	if err != nil {
		return err
	}
	discoveryPath, err := config.DiscoveryPath(opts.Server)
	if err != nil {
		return err
	}
	if _, err := config.EnsureStateDir(opts.Server); err != nil {
		return err
	}

	_, err = oauth.Login(oauth.LoginConfig{
		ServerName:    opts.Server,
		ServerURL:     srv.URL,
		Settings:      settings,
		TokensPath:    tokensPath,
		DiscoveryPath: discoveryPath,
		Out:           opts.Out,
	})
	return err
}

// LogoutOptions describes which stored login to discard.
type LogoutOptions struct {
	ConfigPath string
	Server     string
	Out        io.Writer
}

// Logout deletes a server's stored tokens.
//
// The discovery cache is kept. It holds the client registered with the
// provider, not the user's credentials, and discarding it would create a
// second client record on the provider at the next login for no benefit.
func Logout(opts LogoutOptions) error {
	// The server is resolved first so that logging out of a name that is not
	// configured is reported as the typo it usually is.
	if _, err := loadServer(opts.ConfigPath, opts.Server); err != nil {
		return err
	}

	tokensPath, err := config.TokensPath(opts.Server)
	if err != nil {
		return err
	}
	existed := fileExists(tokensPath)
	if err := oauth.DeleteTokens(tokensPath); err != nil {
		return err
	}

	if existed {
		fmt.Fprintf(opts.Out, "Logged out of %s.\n", opts.Server)
	} else {
		fmt.Fprintf(opts.Out, "No stored login for %s; nothing to do.\n", opts.Server)
	}
	return nil
}
