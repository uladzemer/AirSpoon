//go:build !windows

package airplay

import "fmt"

const protectCredentials = false

func protectCredentialSecrets(data []byte) ([]byte, error) {
	return data, nil
}

func unprotectCredentialSecrets(data []byte) ([]byte, error) {
	return nil, fmt.Errorf("Windows DPAPI credentials cannot be decrypted on this platform")
}
