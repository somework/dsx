package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Where Claude Code keeps its OAuth material, and how dsx finds it.
//
// All of the below was read out of the shipped binary (v2.1.211) rather than
// guessed at. The storage layer is
//
//	qc() = Kac(Bwi, MBn)   // "keychain-with-plaintext-fallback"
//
// which tries the macOS keychain and, if it yields nothing, a plaintext JSON
// file -- on every platform, with no check on process.platform. Both backends
// serialise the same object, so both hold the same {"claudeAiOauth":{...}}.
//
// Measured on a real install: the keychain item is service
// "Claude Code-credentials", account $USER.
//
// NOT verified: no Linux machine was available, so the file lane has never been
// read from a file Claude Code actually wrote. The derivation is from Claude
// Code's own code, but it is a derivation. See PROTOCOL.md.
const credentialsFileName = ".credentials.json"

// errNoCredentials means a store holds no login -- distinct from a store that
// holds a broken one. The chain falls through the first and stops on the
// second, because silently skipping a corrupt keychain would report "not
// signed in" to a user who is.
var errNoCredentials = errors.New("no stored credentials")

type oauthCreds struct {
	AccessToken string   `json:"accessToken"`
	ExpiresAt   int64    `json:"expiresAt"`
	Scopes      []string `json:"scopes"`
}

type keychainBlob struct {
	ClaudeAiOauth oauthCreds `json:"claudeAiOauth"`
}

// envLookup has os.LookupEnv's signature. Claude Code distinguishes an unset
// variable from one set to the empty string, so dsx has to see the difference
// too.
type envLookup func(string) (string, bool)

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// claudeConfigDir mirrors Claude Code's resolution, in its order:
//
//	CLAUDE_SECURESTORAGE_CONFIG_DIR, if defined -- empty means the default
//	CLAUDE_CONFIG_DIR
//	~/.claude
func claudeConfigDir(lookup envLookup, home string) string {
	if v, ok := lookup("CLAUDE_SECURESTORAGE_CONFIG_DIR"); ok {
		if v != "" {
			return v
		}
		return filepath.Join(home, ".claude")
	}
	if v, _ := lookup("CLAUDE_CONFIG_DIR"); v != "" {
		return v
	}
	return filepath.Join(home, ".claude")
}

func credentialsPath(lookup envLookup, home string) string {
	return filepath.Join(claudeConfigDir(lookup, home), credentialsFileName)
}

// keychainServiceName reproduces Claude Code's service name.
//
// The name is computed, not constant: a login under a non-default config dir
// gets a sha256(dir)[:8] suffix so several logins can share one keychain.
// Hardcoding the plain name -- which dsx used to do -- reads the wrong item, or
// none, for anyone who sets CLAUDE_CONFIG_DIR.
//
// The OAUTH_FILE_SUFFIX that sits between "Claude Code" and "-credentials" is
// "" for the production build and non-empty only for Anthropic's internal local
// and custom-endpoint builds; dsx targets the production one.
func keychainServiceName(lookup envLookup, home string) string {
	const base = "Claude Code" + "-credentials"

	var (
		hashOver  string
		isDefault bool
	)
	if v, ok := lookup("CLAUDE_SECURESTORAGE_CONFIG_DIR"); ok {
		hashOver, isDefault = v, v == ""
	} else {
		v, _ := lookup("CLAUDE_CONFIG_DIR")
		hashOver, isDefault = claudeConfigDir(lookup, home), v == ""
	}
	if isDefault {
		return base
	}
	sum := sha256.Sum256([]byte(hashOver))
	return base + "-" + hex.EncodeToString(sum[:])[:8]
}

// readCredentialsFile reads the plaintext store. Absence is not a failure: it
// is how a keychain-backed install looks, and the chain must fall through it.
func readCredentialsFile(path string) (oauthCreds, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return oauthCreds{}, errNoCredentials
	}
	if err != nil {
		return oauthCreds{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var blob keychainBlob
	if err := json.Unmarshal(b, &blob); err != nil {
		return oauthCreds{}, fmt.Errorf("%s is not the JSON Claude Code writes: %w", path, err)
	}
	if blob.ClaudeAiOauth.AccessToken == "" {
		return oauthCreds{}, errNoCredentials
	}
	return blob.ClaudeAiOauth, nil
}

// credSource names the store a login came out of. `dsx doctor` reports it, and
// a user debugging "why does dsx not see my login" needs it more than anything
// else dsx could print.
type credSource string

const (
	srcNone     credSource = "none"
	srcKeychain credSource = "keychain"
	srcFile     credSource = "file"
)

// readCredentialsChain walks the stores in Claude Code's own order and names
// the one that answered.
func readCredentialsChain(keychain func() (oauthCreds, error), filePath string) (oauthCreds, credSource, error) {
	c, err := keychain()
	if err == nil {
		return c, srcKeychain, nil
	}
	if !errors.Is(err, errNoCredentials) {
		return oauthCreds{}, srcNone, err
	}
	c, err = readCredentialsFile(filePath)
	if err != nil {
		return oauthCreds{}, srcNone, err
	}
	return c, srcFile, nil
}

// readCredentials resolves the stored login the way `claude` itself would.
func readCredentials() (oauthCreds, credSource, error) {
	home := homeDir()
	service := keychainServiceName(os.LookupEnv, home)
	return readCredentialsChain(
		func() (oauthCreds, error) { return readKeychain(service) },
		credentialsPath(os.LookupEnv, home),
	)
}

// loadToken resolves the bearer token for the design MCP endpoint.
//
// The token is read, never written. Refreshing here would rotate the refresh
// token out from under Claude Code and silently break its login, so an expired
// token is reported rather than renewed.
func loadToken() (string, error) {
	return tokenFrom(os.LookupEnv, func() (oauthCreds, error) {
		c, _, err := readCredentials()
		return c, err
	})
}

func tokenFrom(lookup envLookup, read func() (oauthCreds, error)) (string, error) {
	if t, _ := lookup("DSX_TOKEN"); t != "" {
		return t, nil
	}

	c, err := read()
	if errors.Is(err, errNoCredentials) {
		return "", &dsxError{Kind: kindAuth,
			Msg: "no Claude Code login found — run `claude` once to sign in, or set DSX_TOKEN"}
	}
	if err != nil {
		return "", &dsxError{Kind: kindAuth, Msg: "reading Claude Code's credentials failed", Err: err}
	}

	if c.ExpiresAt > 0 {
		if exp := time.UnixMilli(c.ExpiresAt); time.Now().After(exp) {
			return "", &dsxError{Kind: kindAuth, Msg: fmt.Sprintf(
				"access token expired at %s — run any `claude` command to refresh it, then retry",
				exp.Format(time.RFC3339))}
		}
	}
	return c.AccessToken, nil
}

// tokenInfo reports non-secret metadata about the stored credential. It must
// never return, log, or render the token itself.
func tokenInfo() (scopes []string, expiresAt time.Time, err error) {
	c, _, err := readCredentials()
	if err != nil {
		return nil, time.Time{}, err
	}
	return c.Scopes, time.UnixMilli(c.ExpiresAt), nil
}
