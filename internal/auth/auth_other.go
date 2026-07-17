//go:build !darwin

package auth

func readKeychain(string) (Creds, error) { return Creds{}, ErrNoCredentials }
