package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// readKeychain fetches Claude Code's credential blob from the login keychain.
//
// The account is not passed. Claude Code writes the item under
// `process.env.USER || os.userInfo().username`, and dsx can be running under a
// shell where USER disagrees with the account that stored it (sudo, a launchd
// job, a container). Matching on the service alone finds the item in every one
// of those cases; the service name already encodes which config dir the login
// belongs to, so it is specific enough on its own.
func readKeychain(service string) (Creds, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", service, "-w").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// security(1) exits non-zero both for "no such item" and for a
			// locked keychain. Only the first means "not signed in"; reporting
			// a locked keychain as an absent login would send the user off to
			// re-run `claude` when the real fix is to unlock.
			if strings.Contains(string(ee.Stderr), "could not be found") {
				return Creds{}, ErrNoCredentials
			}
			return Creds{}, fmt.Errorf("keychain read failed: %s — unlock the login keychain, or set DSX_TOKEN",
				strings.TrimSpace(string(ee.Stderr)))
		}
		return Creds{}, fmt.Errorf("running security(1) failed: %w", err)
	}

	var blob keychainBlob
	if err := json.Unmarshal(out, &blob); err != nil {
		return Creds{}, fmt.Errorf("keychain payload is not the JSON Claude Code writes: %w", err)
	}
	if blob.ClaudeAiOauth.AccessToken == "" {
		return Creds{}, ErrNoCredentials
	}
	return blob.ClaudeAiOauth, nil
}
