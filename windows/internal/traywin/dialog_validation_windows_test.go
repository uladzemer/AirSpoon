package traywin

import (
	"strings"
	"testing"
)

func TestCredentialValidation(t *testing.T) {
	pin, password := pinSpec(false), passwordSpec(true)
	if !password.password || password.numeric || password.limit != 64 || password.errText == "" {
		t.Fatalf("password dialog: %+v", password)
	}
	for _, tc := range []struct {
		text          string
		pin, password bool
	}{
		{"", false, false},
		{"123", false, true},
		{"1234", true, true},
		{"12345", false, true},
		{"12ab", false, true},
		{"１２３４", false, true},
		{" пароль ! ", false, true},
		{" ", false, true},
		{strings.Repeat("я", 64), false, true},
		{strings.Repeat("x", 65), false, false},
	} {
		if pin.valid(tc.text) != tc.pin || password.valid(tc.text) != tc.password {
			t.Errorf("validation %q: PIN=%v password=%v", tc.text, pin.valid(tc.text), password.valid(tc.text))
		}
	}
}
