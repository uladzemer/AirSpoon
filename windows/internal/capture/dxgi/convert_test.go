package dxgi

import (
	"bytes"
	"testing"
)

func TestFitRect(t *testing.T) {
	cases := []struct {
		name           string
		sw, sh, dw, dh int
		want           Rect
	}{
		{"точно по размеру", 1280, 720, 1280, 720, Rect{0, 0, 1280, 720}},
		{"16:9 больше", 1920, 1080, 1280, 720, Rect{0, 0, 1280, 720}},
		{"ultrawide 3440x1440", 3440, 1440, 1280, 720, Rect{0, 92, 1280, 536}},
		{"4:3 даёт поля слева и справа", 1024, 768, 1280, 720, Rect{160, 0, 960, 720}},
		{"портрет", 720, 1280, 1280, 720, Rect{438, 0, 404, 720}},
		{"меньше холста растягивается", 640, 360, 1280, 720, Rect{0, 0, 1280, 720}},
	}
	for _, c := range cases {
		got := FitRect(c.sw, c.sh, c.dw, c.dh)
		if got != c.want {
			t.Errorf("%s: FitRect(%d,%d,%d,%d) = %+v, ожидалось %+v", c.name, c.sw, c.sh, c.dw, c.dh, got, c.want)
		}
		if got.X%2 != 0 || got.Y%2 != 0 || got.W%2 != 0 || got.H%2 != 0 {
			t.Errorf("%s: размеры и смещение должны быть чётными: %+v", c.name, got)
		}
		if got.X < 0 || got.Y < 0 || got.Right() > c.dw || got.Bottom() > c.dh {
			t.Errorf("%s: прямоугольник %+v вне холста %dx%d", c.name, got, c.dw, c.dh)
		}
	}
}

func TestFitRectDegenerate(t *testing.T) {
	got := FitRect(0, 0, 1280, 720)
	if got.W != 1280 || got.H != 720 {
		t.Fatalf("для нулевого источника ожидался весь холст, получено %+v", got)
	}
}

// solidBGRA возвращает кадр одного цвета.
func solidBGRA(w, h int, b, g, r byte) []byte {
	p := make([]byte, w*h*4)
	for i := 0; i < w*h; i++ {
		p[i*4], p[i*4+1], p[i*4+2], p[i*4+3] = b, g, r, 255
	}
	return p
}

func yuvAt(nv12 []byte, w, h, x, y int) (yy, cb, cr byte) {
	yy = nv12[y*w+x]
	uv := w*h + (y/2)*w + (x/2)*2
	return yy, nv12[uv], nv12[uv+1]
}

func TestConvertSolidColors(t *testing.T) {
	cases := []struct {
		name      string
		b, g, r   byte
		y, cb, cr int
	}{
		{"чёрный", 0, 0, 0, 16, 128, 128},
		{"белый", 255, 255, 255, 235, 128, 128},
		{"красный", 0, 0, 255, 63, 102, 240},
		{"зелёный", 0, 255, 0, 173, 42, 26},
		{"синий", 255, 0, 0, 32, 240, 118},
	}
	const w, h = 64, 36
	for _, c := range cases {
		dst := make([]byte, NV12Size(w, h))
		src := solidBGRA(128, 72, c.b, c.g, c.r)
		ConvertBGRAToNV12(dst, w, h, src, 128*4, 128, 72, FitRect(128, 72, w, h))
		y, cb, cr := yuvAt(dst, w, h, 10, 10)
		if abs(int(y)-c.y) > 2 || abs(int(cb)-c.cb) > 2 || abs(int(cr)-c.cr) > 2 {
			t.Errorf("%s: YCbCr = %d,%d,%d, ожидалось около %d,%d,%d", c.name, y, cb, cr, c.y, c.cb, c.cr)
		}
	}
}

func TestConvertLetterboxBars(t *testing.T) {
	const dw, dh = 64, 36
	// Исходник 4:3 на холсте 16:9: поля слева и справа должны быть чёрными.
	const sw, sh = 48, 36
	fit := FitRect(sw, sh, dw, dh)
	if fit.X == 0 {
		t.Fatalf("тест рассчитан на поля слева и справа, fit=%+v", fit)
	}
	dst := make([]byte, NV12Size(dw, dh))
	for i := range dst {
		dst[i] = 77 // мусор: поля должны быть перезаписаны
	}
	src := solidBGRA(sw, sh, 255, 255, 255)
	ConvertBGRAToNV12(dst, dw, dh, src, sw*4, sw, sh, fit)
	y, cb, cr := yuvAt(dst, dw, dh, 0, 10)
	if y != 16 || cb != 128 || cr != 128 {
		t.Errorf("левое поле: %d,%d,%d, ожидалось чёрное 16,128,128", y, cb, cr)
	}
	y, _, _ = yuvAt(dst, dw, dh, dw-1, 10)
	if y != 16 {
		t.Errorf("правое поле: Y=%d, ожидалось 16", y)
	}
	y, _, _ = yuvAt(dst, dw, dh, dw/2, dh/2)
	if y < 230 {
		t.Errorf("центр кадра: Y=%d, ожидалось белое около 235", y)
	}
}

func TestConvertDownscaleAverages(t *testing.T) {
	// Левая половина исходника белая, правая чёрная: после уменьшения граница
	// остаётся на середине, а значения внутри половин не смешиваются.
	const sw, sh, dw, dh = 128, 72, 32, 18
	src := make([]byte, sw*sh*4)
	for y := 0; y < sh; y++ {
		for x := 0; x < sw; x++ {
			v := byte(0)
			if x < sw/2 {
				v = 255
			}
			p := (y*sw + x) * 4
			src[p], src[p+1], src[p+2], src[p+3] = v, v, v, 255
		}
	}
	dst := make([]byte, NV12Size(dw, dh))
	ConvertBGRAToNV12(dst, dw, dh, src, sw*4, sw, sh, Rect{0, 0, dw, dh})
	if y, _, _ := yuvAt(dst, dw, dh, 3, 5); y < 230 {
		t.Errorf("левая половина: Y=%d", y)
	}
	if y, _, _ := yuvAt(dst, dw, dh, dw-4, 5); y > 20 {
		t.Errorf("правая половина: Y=%d", y)
	}
}

func TestFillBlackNV12(t *testing.T) {
	dst := bytes.Repeat([]byte{9}, NV12Size(8, 4))
	FillBlackNV12(dst, 8, 4)
	if dst[0] != 16 || dst[8*4-1] != 16 || dst[8*4] != 128 || dst[len(dst)-1] != 128 {
		t.Fatalf("неверная заливка: %v", dst)
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
