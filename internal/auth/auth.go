package auth

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

	"github.com/somework/dsx/internal/dsxerr"
)

const credentialsFileName = ".credentials.json"

// MsgNoLogin is the user-facing guidance shown when no Claude Code login can
// be found. It is shared verbatim between authErr and `dsx doctor`'s
// credentials check so the two surfaces cannot drift.
const MsgNoLogin = "no Claude Code login found — run `claude` once to sign in, or set DSX_TOKEN"

var ErrNoCredentials = errors.New("no stored credentials")

type Creds struct {
	AccessToken string   `json:"accessToken"`
	ExpiresAt   int64    `json:"expiresAt"`
	Scopes      []string `json:"scopes"`
}

type keychainBlob struct {
	ClaudeAiOauth Creds `json:"claudeAiOauth"`
}

type envLookup func(string) (string, bool)

func HomeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

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

func CredentialsPath(lookup envLookup, home string) string {
	return filepath.Join(claudeConfigDir(lookup, home), credentialsFileName)
}

func KeychainServiceName(lookup envLookup, home string) string {
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

func readCredentialsFile(path string) (Creds, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Creds{}, ErrNoCredentials
	}
	if err != nil {
		return Creds{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var blob keychainBlob
	if err := json.Unmarshal(b, &blob); err != nil {
		return Creds{}, fmt.Errorf("%s is not the JSON Claude Code writes: %w", path, err)
	}
	if blob.ClaudeAiOauth.AccessToken == "" {
		return Creds{}, ErrNoCredentials
	}
	return blob.ClaudeAiOauth, nil
}

type Source string

const (
	SrcNone     Source = "none"
	SrcKeychain Source = "keychain"
	SrcFile     Source = "file"
)

func readCredentialsChain(keychain func() (Creds, error), filePath string) (Creds, Source, error) {
	c, err := keychain()
	if err == nil {
		return c, SrcKeychain, nil
	}
	if !errors.Is(err, ErrNoCredentials) {
		return Creds{}, SrcNone, err
	}
	c, err = readCredentialsFile(filePath)
	if err != nil {
		return Creds{}, SrcNone, err
	}
	return c, SrcFile, nil
}

func ReadCredentials() (Creds, Source, error) {
	home := HomeDir()
	service := KeychainServiceName(os.LookupEnv, home)
	return readCredentialsChain(
		func() (Creds, error) { return readKeychain(service) },
		CredentialsPath(os.LookupEnv, home),
	)
}

func LoadToken() (string, error) {
	return tokenFrom(os.LookupEnv, func() (Creds, error) {
		c, _, err := ReadCredentials()
		return c, err
	})
}

func tokenFrom(lookup envLookup, read func() (Creds, error)) (string, error) {
	if t, _ := lookup("DSX_TOKEN"); t != "" {
		return t, nil
	}

	c, err := read()
	if err != nil {
		return "", authErr(err)
	}

	if c.ExpiresAt > 0 {
		if exp := time.UnixMilli(c.ExpiresAt); time.Now().After(exp) {
			return "", &dsxerr.Error{Kind: dsxerr.KindAuth, Msg: fmt.Sprintf(
				"access token expired at %s — run any `claude` command to refresh it, then retry",
				exp.Format(time.RFC3339))}
		}
	}
	return c.AccessToken, nil
}

func TokenInfo() (scopes []string, expiresAt time.Time, err error) {
	c, _, err := ReadCredentials()
	if err != nil {
		return nil, time.Time{}, authErr(err)
	}
	return c.Scopes, time.UnixMilli(c.ExpiresAt), nil
}

// authErr maps a credentials read/parse failure to a leak-free KindAuth error.
// The underlying cause can carry the credentials file path (hence the OS
// username) or raw token bytes, so it is never wrapped — invariant 8.
func authErr(err error) error {
	if errors.Is(err, ErrNoCredentials) {
		return &dsxerr.Error{Kind: dsxerr.KindAuth, Msg: MsgNoLogin}
	}
	return &dsxerr.Error{Kind: dsxerr.KindAuth,
		Msg: "Claude Code credentials are unreadable — unlock the login keychain, run `claude` to re-authenticate, or set DSX_TOKEN"}
}
