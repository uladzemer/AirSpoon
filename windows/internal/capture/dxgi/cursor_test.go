package dxgi

import "testing"

func TestBlendCursorColor(t *testing.T) {
	// Фон 4x4 серый; указатель 2x2: непрозрачный красный, полупрозрачный белый,
	// полностью прозрачный и ещё один непрозрачный синий.
	region := solidBGRA(4, 4, 100, 100, 100)
	shape := &CursorShape{
		Type: CursorColor, Width: 2, Height: 2, Pitch: 8,
		Data: []byte{
			0, 0, 255, 255, 255, 255, 255, 128,
			1, 2, 3, 0, 255, 0, 0, 255,
		},
	}
	BlendCursor(region, 16, 4, 4, 1, 1, shape)
	px := func(x, y int) [3]byte {
		p := (y*4 + x) * 4
		return [3]byte{region[p], region[p+1], region[p+2]}
	}
	if got := px(1, 1); got != [3]byte{0, 0, 255} {
		t.Errorf("непрозрачный красный: %v", got)
	}
	if got := px(2, 1); got[0] < 175 || got[0] > 180 {
		t.Errorf("полупрозрачный белый поверх 100 должен дать около 178: %v", got)
	}
	if got := px(1, 2); got != [3]byte{100, 100, 100} {
		t.Errorf("прозрачный пиксель не должен менять фон: %v", got)
	}
	if got := px(2, 2); got != [3]byte{255, 0, 0} {
		t.Errorf("непрозрачный синий: %v", got)
	}
	if got := px(0, 0); got != [3]byte{100, 100, 100} {
		t.Errorf("вне указателя фон не меняется: %v", got)
	}
}

func TestBlendCursorMonochrome(t *testing.T) {
	// Монохромный 8x1: верхняя строка — маска AND, нижняя — XOR.
	// Биты слева направо: x=0 AND=0,XOR=1 (белый); x=1 AND=1,XOR=0 (прозрачный);
	// x=2 AND=1,XOR=1 (инверсия); x=3 AND=0,XOR=0 (чёрный).
	shape := &CursorShape{
		Type: CursorMonochrome, Width: 8, Height: 2, Pitch: 1,
	}
	// AND: 0 1 1 0 ...; XOR: 1 0 1 0 ...
	shape.Data = []byte{0b01100000, 0b10100000}
	region := solidBGRA(8, 1, 100, 100, 100)
	BlendCursor(region, 32, 8, 1, 0, 0, shape)
	get := func(x int) byte { return region[x*4] }
	if get(0) != 255 {
		t.Errorf("AND=0,XOR=1 должно давать белый: %d", get(0))
	}
	if get(1) != 100 {
		t.Errorf("AND=1,XOR=0 не меняет фон: %d", get(1))
	}
	if get(2) != 155 {
		t.Errorf("AND=1,XOR=1 инвертирует: %d", get(2))
	}
	if get(3) != 0 {
		t.Errorf("AND=0,XOR=0 даёт чёрный: %d", get(3))
	}
}

func TestBlendCursorMasked(t *testing.T) {
	region := solidBGRA(2, 1, 0x0F, 0x0F, 0x0F)
	shape := &CursorShape{
		Type: CursorMaskedColor, Width: 2, Height: 1, Pitch: 8,
		Data: []byte{0xAA, 0xBB, 0xCC, 0x00, 0xF0, 0xF0, 0xF0, 0xFF},
	}
	BlendCursor(region, 8, 2, 1, 0, 0, shape)
	if region[0] != 0xAA || region[1] != 0xBB || region[2] != 0xCC {
		t.Errorf("маска 0 заменяет цвет: % x", region[0:3])
	}
	if region[4] != 0xFF || region[5] != 0xFF || region[6] != 0xFF {
		t.Errorf("маска FF выполняет XOR: % x", region[4:7])
	}
}

func TestBlendCursorClipping(t *testing.T) {
	// Указатель частично выходит за участок слева-сверху и справа-снизу: паники нет.
	region := solidBGRA(3, 3, 10, 10, 10)
	shape := &CursorShape{Type: CursorColor, Width: 4, Height: 4, Pitch: 16, Data: make([]byte, 64)}
	for i := 0; i < 64; i += 4 {
		shape.Data[i+2], shape.Data[i+3] = 200, 255
	}
	BlendCursor(region, 12, 3, 3, -2, -2, shape)
	BlendCursor(region, 12, 3, 3, 2, 2, shape)
	if region[2] != 200 {
		t.Errorf("пересечение должно быть закрашено: %d", region[2])
	}
}

func TestCursorShapeValid(t *testing.T) {
	bad := []*CursorShape{
		nil,
		{Type: CursorColor, Width: 4, Height: 4, Pitch: 8, Data: make([]byte, 100)},
		{Type: CursorColor, Width: 4, Height: 4, Pitch: 16, Data: make([]byte, 10)},
		{Type: CursorMonochrome, Width: 8, Height: 3, Pitch: 1, Data: make([]byte, 10)},
		{Type: 9, Width: 1, Height: 1, Pitch: 4, Data: make([]byte, 4)},
	}
	for i, s := range bad {
		if s.Valid() {
			t.Errorf("форма %d признана верной", i)
		}
	}
	if w, h := (&CursorShape{Type: CursorMonochrome, Width: 32, Height: 64}).Size(); w != 32 || h != 32 {
		t.Errorf("размер монохромного указателя: %dx%d", w, h)
	}
}
