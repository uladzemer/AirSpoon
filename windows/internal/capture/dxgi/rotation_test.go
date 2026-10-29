package dxgi

import (
	"bytes"
	"fmt"
	"testing"
)

func TestRotationGeometry(t *testing.T) {
	// Native 8x6; points include all corners and an asymmetric interior point.
	points := [][2]int{{0, 0}, {7, 0}, {0, 5}, {7, 5}, {2, 1}}
	for _, tt := range []struct {
		rotation uint32
		w, h     int
		points   [][2]int
		rect     Rect // inverse of logical cursor rectangle (1,2,3,2)
	}{
		{0, 8, 6, points, Rect{1, 2, 3, 2}},
		{dxgiModeRotationIdentity, 8, 6, points, Rect{1, 2, 3, 2}},
		{dxgiModeRotation90, 6, 8, [][2]int{{5, 0}, {5, 7}, {0, 0}, {0, 7}, {4, 2}}, Rect{2, 2, 2, 3}},
		{dxgiModeRotation180, 8, 6, [][2]int{{7, 5}, {0, 5}, {7, 0}, {0, 0}, {5, 4}}, Rect{4, 2, 3, 2}},
		{dxgiModeRotation270, 6, 8, [][2]int{{0, 7}, {0, 0}, {5, 7}, {5, 0}, {1, 5}}, Rect{4, 1, 2, 3}},
	} {
		t.Run(fmt.Sprint(tt.rotation), func(t *testing.T) {
			w, h := rotatedSize(8, 6, tt.rotation)
			if w != tt.w || h != tt.h {
				t.Fatalf("size %dx%d, want %dx%d", w, h, tt.w, tt.h)
			}
			for i, p := range points {
				x, y := rotatePoint(p[0], p[1], 8, 6, tt.rotation)
				if [2]int{x, y} != tt.points[i] {
					t.Errorf("point %v -> (%d,%d), want %v", p, x, y, tt.points[i])
				}
				if sx, sy := rotatePoint(x, y, w, h, inverseRotation(tt.rotation)); sx != p[0] || sy != p[1] {
					t.Errorf("inverse (%d,%d), want %v", sx, sy, p)
				}
			}
			if got := desktopRectToSurface(Rect{1, 2, 3, 2}, 8, 6, tt.rotation); got != tt.rect {
				t.Errorf("cursor rect %+v, want %+v", got, tt.rect)
			}
			// Partially offscreen cursor must be transformed BEFORE clipping.
			for _, r := range []Rect{{-2, -1, 3, 2}, {w - 1, h - 1, 3, 2}} {
				s := desktopRectToSurface(r, 8, 6, tt.rotation)
				for y := s.Y; y < s.Bottom(); y++ {
					for x := s.X; x < s.Right(); x++ {
						dx, dy := rotatePoint(x, y, 8, 6, tt.rotation)
						if dx < r.X || dx >= r.Right() || dy < r.Y || dy >= r.Bottom() {
							t.Fatalf("transformed cursor pixel (%d,%d) outside %+v", dx, dy, r)
						}
					}
				}
			}
		})
	}
}

func TestRotatedDesktopFit(t *testing.T) {
	for _, rotation := range []uint32{1, 2, 3, 4} {
		w, h := rotatedSize(1920, 1080, rotation)
		fit := FitRect(w, h, 1920, 1080)
		want := Rect{0, 0, 1920, 1080}
		if rotation == 2 || rotation == 4 {
			want = Rect{656, 0, 608, 1080}
		}
		if fit != want {
			t.Errorf("rotation %d fit %+v, want %+v", rotation, fit, want)
		}
	}
}

func TestConvertRotatedBGRA(t *testing.T) {
	// Asymmetric source with row padding. The oracle explicitly lays out the
	// upright pixels; it does not call any rotation geometry being tested.
	const w, h, stride = 4, 2, 24
	src := make([]byte, stride*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			n := byte(y*w + x + 1)
			copy(src[y*stride+x*4:], []byte{n * 29, n * 17, n * 11, 255})
		}
	}
	for _, tt := range []struct {
		rotation uint32
		w, h     int
		order    []int
	}{
		{1, 4, 2, []int{1, 2, 3, 4, 5, 6, 7, 8}},
		{2, 2, 4, []int{5, 1, 6, 2, 7, 3, 8, 4}},
		{3, 4, 2, []int{8, 7, 6, 5, 4, 3, 2, 1}},
		{4, 2, 4, []int{4, 8, 3, 7, 2, 6, 1, 5}},
	} {
		upright := make([]byte, w*h*4)
		for i, n := range tt.order {
			p := (n-1)/w*stride + (n-1)%w*4
			copy(upright[i*4:], src[p:p+4])
		}
		for _, size := range [][2]int{{tt.w, tt.h}, {16, 10}, {2, 2}} {
			dw, dh := size[0], size[1]
			fit := FitRect(tt.w, tt.h, dw, dh)
			got, want := make([]byte, NV12Size(dw, dh)), make([]byte, NV12Size(dw, dh))
			convertRotatedBGRAToNV12(got, dw, dh, src, stride, w, h, fit, tt.rotation)
			ConvertBGRAToNV12(want, dw, dh, upright, tt.w*4, tt.w, tt.h, fit)
			if !bytes.Equal(got, want) {
				t.Errorf("rotation %d, canvas %v: NV12 differs\ngot  %v\nwant %v", tt.rotation, size, got, want)
			}
		}
	}
}

func TestCursorRotationComposition(t *testing.T) {
	for _, typ := range []uint32{CursorColor, CursorMaskedColor, CursorMonochrome} {
		s := &CursorShape{Type: typ, Width: 3, Height: 2, Pitch: 16, HotX: 2, HotY: 1}
		if typ == CursorMonochrome {
			s.Height, s.Pitch = 4, 2
			s.Data = []byte{0xA0, 0, 0x60, 0, 0xC0, 0, 0x20, 0}
		} else {
			s.Data = make([]byte, s.Pitch*s.Height)
			for y := 0; y < 2; y++ {
				for x := 0; x < 3; x++ {
					copy(s.Data[y*s.Pitch+x*4:], []byte{byte(20 + x*50), byte(y * 80), 210, byte((x + y) * 85)})
				}
			}
		}
		for rotation := uint32(1); rotation <= 4; rotation++ {
			t.Run(fmt.Sprintf("type%d/rotation%d", typ, rotation), func(t *testing.T) {
				const sw, sh = 8, 6
				lw, lh := rotatedSize(sw, sh, rotation)
				for _, pos := range [][2]int{{1, 2}, {-1, -1}, {lw - 1, lh - 1}} {
					logical := solidBGRA(lw, lh, 47, 81, 123)
					BlendCursor(logical, lw*4, lw, lh, pos[0], pos[1], s)
					native := solidBGRA(sw, sh, 47, 81, 123)
					r := desktopRectToSurface(Rect{pos[0], pos[1], 3, 2}, sw, sh, rotation)
					rs := rotateCursorShape(s, inverseRotation(rotation))
					if !rs.Valid() {
						t.Fatal("invalid rotated shape")
					}
					BlendCursor(native, sw*4, sw, sh, r.X, r.Y, rs)
					for y := 0; y < sh; y++ {
						for x := 0; x < sw; x++ {
							dx, dy := rotatePoint(x, y, sw, sh, rotation)
							if !bytes.Equal(native[(y*sw+x)*4:][:3], logical[(dy*lw+dx)*4:][:3]) {
								t.Fatalf("cursor at %v: pixel (%d,%d) differs", pos, dx, dy)
							}
						}
					}
					// Hotspot is metadata only; preserve it under shape rotation.
					rw, rh := rs.Size()
					hx, hy := rotatePoint(rs.HotX, rs.HotY, rw, rh, rotation)
					if hx != s.HotX || hy != s.HotY {
						t.Errorf("hotspot roundtrip (%d,%d)", hx, hy)
					}
				}
			})
		}
	}
}
