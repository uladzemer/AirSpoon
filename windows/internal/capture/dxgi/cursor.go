package dxgi

// Типы указателя Desktop Duplication (DXGI_OUTDUPL_POINTER_SHAPE_TYPE).
const (
	CursorMonochrome  = 1
	CursorColor       = 2
	CursorMaskedColor = 4
)

// CursorShape — форма указателя из GetFramePointerShape.
type CursorShape struct {
	Type          uint32
	Width, Height int // как в DXGI: у монохромного Height вдвое больше видимой высоты
	Pitch         int
	HotX, HotY    int
	Data          []byte
}

// Size возвращает видимый размер указателя в пикселях.
func (s *CursorShape) Size() (w, h int) {
	if s == nil {
		return 0, 0
	}
	if s.Type == CursorMonochrome {
		return s.Width, s.Height / 2
	}
	return s.Width, s.Height
}

// Valid проверяет, что данные формы соответствуют заявленным размерам.
func (s *CursorShape) Valid() bool {
	if s == nil || s.Width <= 0 || s.Height <= 0 || s.Pitch <= 0 {
		return false
	}
	switch s.Type {
	case CursorMonochrome:
		return s.Height%2 == 0 && s.Pitch*s.Height <= len(s.Data) && s.Pitch*8 >= s.Width
	case CursorColor, CursorMaskedColor:
		return s.Pitch*s.Height <= len(s.Data) && s.Pitch >= s.Width*4
	}
	return false
}

// rotateCursorShape preserves color alpha, masked-color XOR and both monochrome
// bit planes. Used with the inverse desktop rotation before composing on the
// native surface; the final frame rotation makes the pointer upright again.
func rotateCursorShape(s *CursorShape, rotation uint32) *CursorShape {
	if !s.Valid() {
		return nil
	}
	if rotation != dxgiModeRotation90 && rotation != dxgiModeRotation180 && rotation != dxgiModeRotation270 {
		return s
	}
	w, h := s.Size()
	rw, rh := rotatedSize(w, h, rotation)
	r := &CursorShape{Type: s.Type, Width: rw, Height: rh, Pitch: rw * 4}
	r.HotX, r.HotY = rotatePoint(s.HotX, s.HotY, w, h, rotation)
	if s.Type == CursorMonochrome {
		r.Height *= 2
		r.Pitch = (rw + 7) / 8
	}
	r.Data = make([]byte, r.Pitch*r.Height)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			rx, ry := rotatePoint(x, y, w, h, rotation)
			if s.Type == CursorMonochrome {
				for plane := 0; plane < 2; plane++ {
					bit := s.Data[(y+plane*h)*s.Pitch+x/8] >> (7 - uint(x%8)) & 1
					r.Data[(ry+plane*rh)*r.Pitch+rx/8] |= bit << (7 - uint(rx%8))
				}
			} else {
				copy(r.Data[ry*r.Pitch+rx*4:][:4], s.Data[y*s.Pitch+x*4:][:4])
			}
		}
	}
	return r
}

// BlendCursor рисует указатель поверх участка кадра BGRA. region — копия
// участка рабочего стола (rw x rh пикселей, строка stride байт), (cx, cy) —
// положение левого верхнего угла указателя относительно участка (может быть
// отрицательным: рисуется только пересечение).
func BlendCursor(region []byte, stride, rw, rh, cx, cy int, s *CursorShape) {
	if !s.Valid() {
		return
	}
	w, h := s.Size()
	for y := 0; y < h; y++ {
		dy := cy + y
		if dy < 0 || dy >= rh {
			continue
		}
		for x := 0; x < w; x++ {
			dx := cx + x
			if dx < 0 || dx >= rw {
				continue
			}
			d := region[dy*stride+dx*4 : dy*stride+dx*4+4]
			switch s.Type {
			case CursorMonochrome:
				// Верхняя половина — маска AND, нижняя — XOR: итог (экран AND a) XOR x.
				andBit := s.Data[y*s.Pitch+x/8] >> (7 - uint(x%8)) & 1
				xorBit := s.Data[(y+h)*s.Pitch+x/8] >> (7 - uint(x%8)) & 1
				var am, xm byte
				if andBit != 0 {
					am = 0xFF
				}
				if xorBit != 0 {
					xm = 0xFF
				}
				d[0] = d[0]&am ^ xm
				d[1] = d[1]&am ^ xm
				d[2] = d[2]&am ^ xm
			case CursorColor:
				p := s.Data[y*s.Pitch+x*4 : y*s.Pitch+x*4+4]
				a := int(p[3])
				if a == 0 {
					continue
				}
				if a == 255 {
					d[0], d[1], d[2] = p[0], p[1], p[2]
					continue
				}
				for k := 0; k < 3; k++ {
					d[k] = byte((int(p[k])*a + int(d[k])*(255-a) + 127) / 255)
				}
			case CursorMaskedColor:
				p := s.Data[y*s.Pitch+x*4 : y*s.Pitch+x*4+4]
				if p[3] == 0 {
					d[0], d[1], d[2] = p[0], p[1], p[2]
				} else {
					d[0] ^= p[0]
					d[1] ^= p[1]
					d[2] ^= p[2]
				}
			}
		}
	}
}
