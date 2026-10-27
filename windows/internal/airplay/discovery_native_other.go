//go:build !windows

package airplay

import (
	"context"
	"errors"
)

// nativeBrowse на других системах недоступен: системный поиск — только Windows.
func nativeBrowse(ctx context.Context, serviceType string) ([]AirPlayDevice, error) {
	return nil, errors.New("системный mDNS есть только в Windows")
}
