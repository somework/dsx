package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// keychainService is where Claude Code stores its OAuth material on macOS.
const keychainService = "Claude Code-credentials"

type oauthCreds struct {
	AccessToken string   `json:"accessToken"`
	ExpiresAt   int64    `json:"expiresAt"`
	Scopes      []string `json:"scopes"`
}

type keychainBlob struct {
	ClaudeAiOauth oauthCreds `json:"claudeAiOauth"`
}

// loadToken resolves the bearer token for the design MCP endpoint.
//
// The token is read, never written. Refreshing here would rotate the refresh
// token out from under Claude Code and silently break its login, so an expired
// token is reported rather than renewed.
func loadToken() (string, error) {
	if t := os.Getenv("DSX_TOKEN"); t != "" {
		return t, nil
	}

	out, err := exec.Command("security", "find-generic-password", "-s", keychainService, "-w").Output()
	if err != nil {
		return "", fmt.Errorf("keychain read failed: %w\nunlock the login keychain, or set DSX_TOKEN", err)
	}

	var blob keychainBlob
	if err := json.Unmarshal(out, &blob); err != nil {
		return "", fmt.Errorf("keychain payload is not JSON: %w", err)
	}

	c := blob.ClaudeAiOauth
	if c.AccessToken == "" {
		return "", fmt.Errorf("no claudeAiOauth.accessToken in keychain — run `claude` once to sign in")
	}
	if c.ExpiresAt > 0 {
		if exp := time.UnixMilli(c.ExpiresAt); time.Now().After(exp) {
			return "", fmt.Errorf(
				"access token expired at %s — run any `claude` command to refresh it, then retry",
				exp.Format(time.RFC3339))
		}
	}
	return c.AccessToken, nil
}

// tokenInfo reports non-secret metadata about the stored credential.
func tokenInfo() (scopes []string, expiresAt time.Time, err error) {
	out, err := exec.Command("security", "find-generic-password", "-s", keychainService, "-w").Output()
	if err != nil {
		return nil, time.Time{}, err
	}
	var blob keychainBlob
	if err := json.Unmarshal(out, &blob); err != nil {
		return nil, time.Time{}, err
	}
	return blob.ClaudeAiOauth.Scopes, time.UnixMilli(blob.ClaudeAiOauth.ExpiresAt), nil
}
