// Package config loads CLI configuration from environment variables.
// All backend services are accessed through a single API base URL
// (OCTO_API_BASE_URL).
package config

import (
	"fmt"
	"os"
)

// Environment variable names. Centralised for testability and discoverability.
const (
	EnvAPIBaseURL = "OCTO_API_BASE_URL"
	// EnvDocAPIBaseURL is the base URL for the octo-docs-html backend (spec:
	// x-octo-base-url on internal/registry/specs/html.json). It is a separate
	// product surface from the Octo Bot API and MUST NOT fall back to
	// OCTO_API_BASE_URL — see ServiceURL fail-loud below.
	EnvDocAPIBaseURL = "OCTO_DOC_API_URL"
	EnvBotToken      = "OCTO_BOT_TOKEN"
	EnvSpaceID       = "OCTO_SPACE_ID"
	EnvFormat        = "OCTO_FORMAT"
	// EnvBotID is the env form of --bot-id: a robot id that selects a stored
	// credential profile. It is a selector, not a secret (cf. EnvBotToken).
	EnvBotID = "OCTO_BOT_ID"
)

// Config holds CLI configuration loaded from environment variables.
// BotToken and SpaceID live here too (duplicated in credential.EnvProvider) so
// callers wiring a client without a full provider chain still have access; the
// credential provider remains the authoritative path for command execution.
type Config struct {
	// APIBaseURL is the unified base URL for all backend services.
	// Set via OCTO_API_BASE_URL.
	APIBaseURL string
	// BotToken is the App Bot token (OCTO_BOT_TOKEN).
	BotToken string
	// SpaceID is the platform-bot space context (OCTO_SPACE_ID). Optional for space-scoped bots.
	SpaceID string
	// Format is the default output format.
	Format string
}

// Load reads configuration from the environment.
func Load() *Config {
	return &Config{
		APIBaseURL: envOrDefault(EnvAPIBaseURL, "http://127.0.0.1:8080"),
		BotToken:   os.Getenv(EnvBotToken),
		SpaceID:    os.Getenv(EnvSpaceID),
		Format:     envOrDefault(EnvFormat, "json"),
	}
}

// Validate checks required fields. Only the token is required at load time;
// space-id validation is deferred to the client (which knows whether the bot
// is space- or platform-scoped from the spec).
func (c *Config) Validate() error {
	if c.BotToken == "" {
		return fmt.Errorf("%s is required (App Bot token, app_*)", EnvBotToken)
	}
	return nil
}

// ServiceURL returns the base URL for a service key. The key is either the
// spec-declared x-octo-base-url env var name (a well-formed "OCTO_*"
// identifier routed by serviceForBaseURL) or a legacy free-form value
// ("matters" / "dmworkim" / "" / "default"). Legacy values keep the unified
// behaviour of mapping to APIBaseURL; only well-formed env-var names route
// to their env value.
//
// For a well-formed spec env name other than EnvAPIBaseURL the method returns
// os.Getenv(envName) verbatim: an empty string is deliberately propagated so
// client.Do can fail loud with a hint pointing at the correct env var.
// Falling back to APIBaseURL would silently route html-domain traffic to the
// bot backend when OCTO_DOC_API_URL is unset — a config-error footgun the CLI
// must surface, not paper over.
func (c *Config) ServiceURL(service string) string {
	if service == EnvAPIBaseURL {
		return c.APIBaseURL
	}
	if isEnvVarName(service) {
		return os.Getenv(service)
	}
	return c.APIBaseURL
}

// isEnvVarName reports whether s looks like a spec-declared x-octo-base-url
// value: uppercase ASCII letters, digits, and underscores, starting with a
// letter, at least 2 chars. Legacy service keys like "matters" / "dmworkim"
// / "" / "default" fail this and keep the historical unified route.
func isEnvVarName(s string) bool {
	if len(s) < 2 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
		case r == '_':
			if i == 0 {
				return false
			}
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
