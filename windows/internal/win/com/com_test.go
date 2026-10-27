//go:build windows

package com

import (
	"errors"
	"testing"
)

func TestParseGUID(t *testing.T) {
	g := ParseGUID("bf94c121-5b05-4e6f-8000-ba598961414d")
	if g.Data1 != 0xbf94c121 || g.Data2 != 0x5b05 || g.Data3 != 0x4e6f || g.Data4[0] != 0x80 || g.Data4[7] != 0x4d {
		t.Fatalf("GUID разобран неверно: %+v", g)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("неверный GUID должен вызывать панику")
		}
	}()
	ParseGUID("не-guid")
}

func TestCheckAndHR(t *testing.T) {
	if err := Check("ok", 0); err != nil {
		t.Fatalf("S_OK: %v", err)
	}
	if err := Check("false", 1); err != nil {
		t.Fatalf("S_FALSE не ошибка: %v", err)
	}
	// Старшие 32 бита значения регистра не определены: учитывается только HRESULT.
	err := Check("op", 0xDEAD_0000_0000_0000|0x887A0026)
	if err == nil {
		t.Fatal("отрицательный HRESULT должен давать ошибку")
	}
	if !Is(err, 0x887A0026) || Is(err, 0x887A0027) {
		t.Fatalf("Is: %v", err)
	}
	if hr, ok := HR(errors.New("чужая")); ok || hr != 0 {
		t.Fatal("HR от чужой ошибки")
	}
	wrapped := errors.Join(errors.New("контекст"), err)
	if !Is(wrapped, 0x887A0026) {
		t.Fatal("Is должен видеть вложенную ошибку")
	}
}

func TestNilObjectRelease(t *testing.T) {
	var o *Object
	o.Release() // nil допустим
	o.AddRef()
}
