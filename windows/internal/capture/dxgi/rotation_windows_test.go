//go:build windows && (amd64 || arm64)

package dxgi

import (
	"testing"
	"time"
	"unsafe"

	"airspoon/internal/win/com"
)

func TestVideoProcessorRotationCaps(t *testing.T) {
	for rotation := uint32(0); rotation <= 4; rotation++ {
		if err := checkVPRotation(vpFeatureCapsRotation, rotation); err != nil {
			t.Errorf("rotation %d with caps: %v", rotation, err)
		}
		if err := checkVPRotation(0, rotation); (err != nil) != (rotation > 1) {
			t.Errorf("rotation %d without caps: %v", rotation, err)
		}
	}
}

func TestTextureToMemoryInvalidatesReadback(t *testing.T) {
	c := &Capturer{wantTexture: true, version: 10, dataVersion: 10, data: []byte{1, 2, 3}}
	if err := c.SetWantTexture(false); err != nil {
		t.Fatal(err)
	}
	if len(c.data) != 0 {
		t.Fatal("texture-to-memory fallback retained a stale readback")
	}
}

// Upload synthetic pixels to the capture device: exercises actual VP rotation,
// GPU readback, CPU fallback, and same-size runtime reconfiguration without
// changing the real monitor orientation or acquiring any desktop images.
func TestSyntheticRotationFrames(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, mode := range []struct {
		name string
		cpu  bool
	}{{"gpu", false}, {"cpu", true}} {
		t.Run(mode.name, func(t *testing.T) {
			c := openTestCapturer(t, Config{Width: 64, Height: 64, FPS: 30, ForceCPU: mode.cpu})
			defer c.Close()
			if !mode.cpu && !c.HasVideoProcessor() {
				t.Skip("видеопроцессор GPU недоступен")
			}
			const w, h = 64, 32
			pixels := make([]byte, w*h*4)
			colors := [][4]byte{{20, 40, 230, 255}, {40, 220, 30, 255}, {210, 30, 40, 255}, {220, 220, 220, 255}}
			// The sequences include 90 -> 270 and 180 -> identity, which have
			// identical dimensions and still must invalidate VP and cached NV12.
			for _, rotation := range []uint32{1, 2, 4, 3, 1} {
				c.noteRotation(rotation)
				if err := c.ensureSize(w, h); err != nil {
					t.Fatal(err)
				}
				if !mode.cpu && !c.HasVideoProcessor() {
					t.Skipf("GPU rotation unavailable: %s", c.pathReason)
				}
				for y := 0; y < h; y++ {
					for x := 0; x < w; x++ {
						color := colors[(y/(h/2))*2+x/(w/2)]
						copy(pixels[(y*w+x)*4:], color[:])
					}
				}
				c.ctx.Call(slotCtxUpdateSubresource, uintptr(unsafe.Pointer(c.desktop)), 0, 0,
					uintptr(unsafe.Pointer(&pixels[0])), w*4, 0)
				c.version++
				c.dirty, c.haveDesktop = true, true
				frame, err := c.produce(time.Now())
				if err != nil {
					t.Fatal(err)
				}
				if !mode.cpu && !c.HasVideoProcessor() {
					t.Fatalf("VideoProcessorBlt fell back to CPU: %s", c.pathReason)
				}
				want := make([]byte, NV12Size(64, 64))
				convertRotatedBGRAToNV12(want, 64, 64, pixels, w*4, w, h, c.fit, rotation)
				checkSyntheticNV12(t, frame.Data, want, c.fit, rotation)
				if !mode.cpu {
					// Change contents while using textures, then switch to memory
					// with no further desktop update (slow texture input fallback).
					if err := c.SetWantTexture(true); err != nil {
						t.Fatal(err)
					}
					for i := 0; i < len(pixels); i += 4 {
						pixels[i], pixels[i+2] = pixels[i+2], pixels[i]
					}
					c.ctx.Call(slotCtxUpdateSubresource, uintptr(unsafe.Pointer(c.desktop)), 0, 0,
						uintptr(unsafe.Pointer(&pixels[0])), w*4, 0)
					c.version++
					frame, err = c.produce(time.Now())
					if err != nil || frame.Texture == nil {
						t.Fatalf("texture frame: %v, %v", frame.Texture, err)
					}
					if err := c.SetWantTexture(false); err != nil {
						t.Fatal(err)
					}
					frame, err = c.produce(time.Now())
					if err != nil {
						t.Fatal(err)
					}
					convertRotatedBGRAToNV12(want, 64, 64, pixels, w*4, w, h, c.fit, rotation)
					checkSyntheticNV12(t, frame.Data, want, c.fit, rotation)
				}
			}
		})
	}
}

func checkSyntheticNV12(t *testing.T, got, want []byte, fit Rect, rotation uint32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("rotation %d: NV12 length %d, want %d", rotation, len(got), len(want))
	}
	// Interior of each quadrant, plus a black letterbox pixel. Avoid filtering
	// boundaries; tolerate fixed-point differences in GPU color conversion.
	points := [][2]int{{0, 0}}
	for _, fy := range []int{1, 3} {
		for _, fx := range []int{1, 3} {
			points = append(points, [2]int{fit.X + fx*fit.W/4, fit.Y + fy*fit.H/4})
		}
	}
	for _, p := range points {
		x, y := p[0], p[1]
		uv := 64*64 + y/2*64 + x/2*2
		for _, offset := range []int{y*64 + x, uv, uv + 1} {
			diff := int(got[offset]) - int(want[offset])
			if diff < -4 || diff > 4 {
				t.Errorf("rotation %d at %v, offset %d: got %d, want %d", rotation, p, offset, got[offset], want[offset])
			}
		}
	}
}
