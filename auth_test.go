package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/somework/dsx/internal/dsxerr"
)

// envMap builds a lookup with os.LookupEnv's semantics: set-but-empty and
// unset are different answers, and Claude Code's own resolution turns on that
// difference.
func envMap(kv map[string]string) envLookup {
	return func(k string) (string, bool) {
		v, ok := kv[k]
		return v, ok
	}
}

func TestClaudeConfigDirMatchesClaudeCodesOwnResolution(t *testing.T) {
	home := filepath.Join("/home", "u")
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"default when nothing is set", nil, filepath.Join(home, ".claude")},
		{"CLAUDE_CONFIG_DIR wins", map[string]string{"CLAUDE_CONFIG_DIR": "/custom"}, "/custom"},
		{"empty CLAUDE_CONFIG_DIR is treated as unset", map[string]string{"CLAUDE_CONFIG_DIR": ""}, filepath.Join(home, ".claude")},
		{"securestorage overrides config dir", map[string]string{
			"CLAUDE_SECURESTORAGE_CONFIG_DIR": "/secure", "CLAUDE_CONFIG_DIR": "/custom",
		}, "/secure"},
		{"securestorage set but empty means the default", map[string]string{
			"CLAUDE_SECURESTORAGE_CONFIG_DIR": "", "CLAUDE_CONFIG_DIR": "/custom",
		}, filepath.Join(home, ".claude")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeConfigDir(envMap(tc.env), home); got != tc.want {
				t.Errorf("claudeConfigDir = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCredentialsPathSitsInTheConfigDir(t *testing.T) {
	got := credentialsPath(envMap(map[string]string{"CLAUDE_CONFIG_DIR": "/custom"}), "/home/u")
	if got != filepath.Join("/custom", ".credentials.json") {
		t.Fatalf("credentials path = %q", got)
	}
}

func TestKeychainServiceNameIsPlainOnADefaultInstall(t *testing.T) {
	// Measured on this machine: `security find-generic-password -s
	// "Claude Code-credentials"` resolves, with neither env var set.
	got := keychainServiceName(envMap(nil), "/home/u")
	if got != "Claude Code-credentials" {
		t.Fatalf("default service = %q, want %q", got, "Claude Code-credentials")
	}
}

func TestKeychainServiceNameHashesANonDefaultConfigDir(t *testing.T) {
	// Claude Code suffixes the service with sha256(dir)[:8] whenever the config
	// dir is not the default, so one machine can hold several logins. Hardcoding
	// the plain name makes dsx read the wrong item -- or none at all.
	got := keychainServiceName(envMap(map[string]string{"CLAUDE_CONFIG_DIR": "/custom"}), "/home/u")
	if got == "Claude Code-credentials" {
		t.Fatal("a custom config dir must not reuse the default service name")
	}
	if !strings.HasPrefix(got, "Claude Code-credentials-") {
		t.Fatalf("service = %q, want the default name plus a hash suffix", got)
	}
	suffix := strings.TrimPrefix(got, "Claude Code-credentials-")
	if len(suffix) != 8 {
		t.Errorf("hash suffix %q is %d chars, want 8", suffix, len(suffix))
	}
	// Same dir must resolve to the same item on every run.
	again := keychainServiceName(envMap(map[string]string{"CLAUDE_CONFIG_DIR": "/custom"}), "/home/u")
	if again != got {
		t.Errorf("service name is not stable: %q then %q", got, again)
	}
	other := keychainServiceName(envMap(map[string]string{"CLAUDE_CONFIG_DIR": "/elsewhere"}), "/home/u")
	if other == got {
		t.Error("two different config dirs collide on one service name")
	}
}

func TestKeychainServiceNameUsesSecureStorageDirForTheHash(t *testing.T) {
	viaSecure := keychainServiceName(envMap(map[string]string{"CLAUDE_SECURESTORAGE_CONFIG_DIR": "/secure"}), "/home/u")
	viaConfig := keychainServiceName(envMap(map[string]string{"CLAUDE_CONFIG_DIR": "/secure"}), "/home/u")
	if viaSecure != viaConfig {
		t.Errorf("the same directory hashed two ways: %q vs %q", viaSecure, viaConfig)
	}
}

func writeCreds(t *testing.T, dir string, c oauthCreds) string {
	t.Helper()
	b, err := json.Marshal(keychainBlob{ClaudeAiOauth: c})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, credentialsFileName)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadCredentialsFileParsesClaudeCodesShape(t *testing.T) {
	dir := t.TempDir()
	writeCreds(t, dir, oauthCreds{AccessToken: "sk-ant-oat01-x", ExpiresAt: 1784000000000, Scopes: []string{"user:mcp_servers"}})

	got, err := readCredentialsFile(filepath.Join(dir, credentialsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "sk-ant-oat01-x" {
		t.Errorf("accessToken = %q", got.AccessToken)
	}
	if len(got.Scopes) != 1 || got.Scopes[0] != "user:mcp_servers" {
		t.Errorf("scopes = %v", got.Scopes)
	}
}

func TestReadCredentialsFileReportsAbsenceAsNoCredentialsNotAsFailure(t *testing.T) {
	_, err := readCredentialsFile(filepath.Join(t.TempDir(), "nope.json"))
	if !errors.Is(err, errNoCredentials) {
		t.Fatalf("missing file gave %v, want errNoCredentials so the chain can fall through", err)
	}
}

func TestReadCredentialsFileRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, credentialsFileName)
	if err := os.WriteFile(p, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readCredentialsFile(p)
	if err == nil {
		t.Fatal("garbage parsed as credentials")
	}
	if errors.Is(err, errNoCredentials) {
		t.Fatal("a corrupt store must not read as an empty one: it would be silently skipped")
	}
}

func TestReadCredentialsFileTreatsAnEmptyTokenAsNoCredentials(t *testing.T) {
	dir := t.TempDir()
	writeCreds(t, dir, oauthCreds{AccessToken: ""})
	_, err := readCredentialsFile(filepath.Join(dir, credentialsFileName))
	if !errors.Is(err, errNoCredentials) {
		t.Fatalf("a blank token gave %v, want errNoCredentials", err)
	}
}

func TestTokenFromPrefersDSXTokenOverEveryStore(t *testing.T) {
	got, err := tokenFrom(envMap(map[string]string{"DSX_TOKEN": "override"}), func() (oauthCreds, error) {
		t.Fatal("the store was consulted despite DSX_TOKEN being set")
		return oauthCreds{}, nil
	})
	if err != nil || got != "override" {
		t.Fatalf("tokenFrom = %q, %v", got, err)
	}
}

func TestTokenFromRejectsAnExpiredTokenAsAuthNotAsSuccess(t *testing.T) {
	past := time.Now().Add(-time.Hour).UnixMilli()
	_, err := tokenFrom(envMap(nil), func() (oauthCreds, error) {
		return oauthCreds{AccessToken: "stale", ExpiresAt: past}, nil
	})
	if err == nil {
		t.Fatal("an expired token was accepted")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindAuth {
		t.Errorf("expired token classified %q, want %q so the caller knows to run `claude`", got, dsxerr.KindAuth)
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("the error must say what to do next: %q", err.Error())
	}
}

func TestTokenFromAcceptsATokenWithNoRecordedExpiry(t *testing.T) {
	got, err := tokenFrom(envMap(nil), func() (oauthCreds, error) {
		return oauthCreds{AccessToken: "live"}, nil
	})
	if err != nil || got != "live" {
		t.Fatalf("tokenFrom = %q, %v", got, err)
	}
}

func TestTokenFromClassifiesAMissingStoreAsAuth(t *testing.T) {
	_, err := tokenFrom(envMap(nil), func() (oauthCreds, error) {
		return oauthCreds{}, errNoCredentials
	})
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindAuth {
		t.Fatalf("absent credentials classified %q, want %q", got, dsxerr.KindAuth)
	}
}

func TestTokenFromNeverPutsTheTokenInAnError(t *testing.T) {
	// The one thing this binary must never do is print the credential.
	const secret = "sk-ant-oat01-SECRET-VALUE"
	_, err := tokenFrom(envMap(nil), func() (oauthCreds, error) {
		return oauthCreds{AccessToken: secret, ExpiresAt: time.Now().Add(-time.Hour).UnixMilli()}, nil
	})
	if err == nil {
		t.Fatal("expected an expiry error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the token leaked into an error message: %q", err.Error())
	}
}

func TestReadCredentialsFallsBackToTheFileWhenTheKeychainHasNothing(t *testing.T) {
	// Measured in Claude Code's own storage layer: Kac(Bwi, MBn) reads the
	// keychain first and the plaintext file second, with no platform gate. dsx
	// has to walk the same chain or it will not find a login claude can.
	dir := t.TempDir()
	writeCreds(t, dir, oauthCreds{AccessToken: "from-file"})

	got, src, err := readCredentialsChain(
		func() (oauthCreds, error) { return oauthCreds{}, errNoCredentials },
		filepath.Join(dir, credentialsFileName),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "from-file" {
		t.Fatalf("accessToken = %q, want the file's", got.AccessToken)
	}
	if src != srcFile {
		t.Errorf("source = %q, want %q", src, srcFile)
	}
}

func TestReadCredentialsPrefersTheKeychainOverTheFile(t *testing.T) {
	dir := t.TempDir()
	writeCreds(t, dir, oauthCreds{AccessToken: "from-file"})

	got, src, err := readCredentialsChain(
		func() (oauthCreds, error) { return oauthCreds{AccessToken: "from-keychain"}, nil },
		filepath.Join(dir, credentialsFileName),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "from-keychain" {
		t.Fatalf("accessToken = %q, want the keychain's", got.AccessToken)
	}
	if src != srcKeychain {
		t.Errorf("source = %q, want %q", src, srcKeychain)
	}
}

func TestReadCredentialsSurfacesACorruptFileRatherThanReportingNoLogin(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, credentialsFileName)
	if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := readCredentialsChain(func() (oauthCreds, error) { return oauthCreds{}, errNoCredentials }, p)
	if err == nil || errors.Is(err, errNoCredentials) {
		t.Fatalf("a corrupt file gave %v; it must be reported, not read as 'not signed in'", err)
	}
}

func TestReadCredentialsChainStopsOnABrokenKeychainRatherThanMaskingIt(t *testing.T) {
	// A locked keychain is not an absent login. Falling through to the file
	// would report "not signed in" and send the user to re-run `claude`, when
	// the real fix is to unlock.
	dir := t.TempDir()
	writeCreds(t, dir, oauthCreds{AccessToken: "from-file"})
	boom := errors.New("keychain is locked")

	_, _, err := readCredentialsChain(
		func() (oauthCreds, error) { return oauthCreds{}, boom },
		filepath.Join(dir, credentialsFileName),
	)
	if !errors.Is(err, boom) {
		t.Fatalf("a broken keychain was masked by the file fallback: %v", err)
	}
}

func TestKeychainIsOnlyConsultedOnDarwin(t *testing.T) {
	// The build-tag split is the point: on Linux there is no security(1), and
	// shelling out to a missing binary on every run is a bug, not a fallback.
	_, err := readKeychain("Claude Code-credentials")
	if runtime.GOOS != "darwin" && !errors.Is(err, errNoCredentials) {
		t.Fatalf("non-darwin keychain read gave %v, want errNoCredentials", err)
	}
}
