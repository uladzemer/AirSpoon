//go:build windows && (amd64 || arm64)

package dxgi

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"airspoon/internal/win/com"
)

// nv12ToImage переводит NV12 (BT.709, ограниченный диапазон) в RGB для просмотра.
func nv12ToImage(data []byte, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			yy := float64(data[y*w+x]) - 16
			uv := w*h + (y/2)*w + (x/2)*2
			cb := float64(data[uv]) - 128
			cr := float64(data[uv+1]) - 128
			r := 1.164*yy + 1.793*cr
			g := 1.164*yy - 0.213*cb - 0.533*cr
			b := 1.164*yy + 2.112*cb
			img.SetRGBA(x, y, color.RGBA{clampByte(int(r)), clampByte(int(g)), clampByte(int(b)), 255})
		}
	}
	return img
}

// TestDumpFrames сохраняет кадры обоих путей (GPU и CPU) в PNG для осмотра;
// включается переменной AIRSPOON_DUMP_DIR.
func TestDumpFrames(t *testing.T) {
	dir := os.Getenv("AIRSPOON_DUMP_DIR")
	if dir == "" {
		t.Skip("AIRSPOON_DUMP_DIR не задан")
	}
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, mode := range []struct {
		name string
		cpu  bool
	}{{"gpu", false}, {"cpu", true}} {
		c := openTestCapturer(t, Config{Width: 1280, Height: 720, FPS: 30, ShowCursor: true, ForceCPU: mode.cpu})
		stop := make(chan struct{})
		var f Frame
		for i := 0; i < 20; i++ {
			if f, err = c.Grab(stop); err != nil {
				t.Fatal(err)
			}
		}
		close(stop)
		out, err := os.Create(filepath.Join(dir, "dxgi_"+mode.name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(out, nv12ToImage(f.Data, 1280, 720)); err != nil {
			t.Fatal(err)
		}
		out.Close()
		c.Close()
	}
}
