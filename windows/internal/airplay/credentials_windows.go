//go:build windows

package airplay

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const protectCredentials = true

func protectCredentialSecrets(data []byte) ([]byte, error) {
	return cryptCredentialSecrets(data, true)
}

func unprotectCredentialSecrets(data []byte) ([]byte, error) {
	return cryptCredentialSecrets(data, false)
}

func cryptCredentialSecrets(data []byte, protect bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty DPAPI input")
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	var err error
	// No LOCAL_MACHINE flag: only the current Windows user can decrypt.
	if protect {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	runtime.KeepAlive(data)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, int(out.Size))...), nil
}
