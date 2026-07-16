//go:build !darwin

package main

// readKeychain has nothing to read off macOS: security(1) is a macOS binary,
// and Claude Code's keychain lane fails there for the same reason, falling
// through to the plaintext file. Reporting "no credentials" rather than
// shelling out to a command that is not installed keeps that fall-through
// honest and silent.
func readKeychain(string) (oauthCreds, error) { return oauthCreds{}, errNoCredentials }
