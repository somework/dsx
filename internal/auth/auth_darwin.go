package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

func readKeychain(service string) (Creds, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", service, "-w").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
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
