package mf

import (
	"errors"
	"fmt"
	"testing"
)

func TestMarkUnavailable(t *testing.T) {
	if markUnavailable(nil) != nil {
		t.Fatal("nil должен остаться nil")
	}
	base := errors.New("MFStartup: 0xC00D36E5")
	err := fmt.Errorf("кодер: %w", markUnavailable(base))
	if !errors.Is(err, ErrUnavailable) {
		t.Error("ошибка должна совпадать с ErrUnavailable")
	}
	if !errors.Is(err, base) {
		t.Error("исходная ошибка должна остаться в цепочке")
	}
	if errors.Is(base, ErrUnavailable) {
		t.Error("обычная ошибка не должна совпадать с ErrUnavailable")
	}
}
