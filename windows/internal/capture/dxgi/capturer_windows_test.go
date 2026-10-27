//go:build windows && (amd64 || arm64)

package dxgi

import (
	"errors"
	"strings"
	"testing"
	"time"

	"airspoon/internal/win/com"
)

// openTestCapturer создаёт захват или пропускает тест, если рабочего стола с
// дублированием нет (сеанс без экрана, удалённый рабочий стол, нет GPU).
func openTestCapturer(t *testing.T, cfg Config) *Capturer {
	t.Helper()
	cfg.Logf = t.Logf
	cfg.Debugf = t.Logf
	c, err := New(cfg)
	if err != nil {
		t.Skipf("захват экрана недоступен в этом окружении: %v", err)
	}
	return c
}

func TestListOutputs(t *testing.T) {
	list, err := ListOutputs()
	if err != nil {
		t.Skipf("перечисление мониторов недоступно: %v", err)
	}
	for _, o := range list {
		t.Logf("монитор %d: %s на %s, %+v", o.Index, o.Name, o.AdapterName, o.Bounds)
	}
}

func TestCapturerMemoryFrames(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	c := openTestCapturer(t, Config{Display: 0, Width: 1280, Height: 720, FPS: 30, ShowCursor: true})
	defer c.Close()
	t.Logf("монитор: %s, GPU-преобразование: %t", c.MonitorInfo(), c.HasVideoProcessor())

	stop := make(chan struct{})
	defer close(stop)
	const frames = 45
	start := time.Now()
	var lastAt time.Time
	nonBlack := false
	for i := 0; i < frames; i++ {
		f, err := c.Grab(stop)
		if err != nil {
			t.Fatalf("Grab #%d: %v", i, err)
		}
		if len(f.Data) != NV12Size(1280, 720) {
			t.Fatalf("размер кадра %d, ожидалось %d", len(f.Data), NV12Size(1280, 720))
		}
		if !lastAt.IsZero() && f.At.Before(lastAt) {
			t.Fatalf("метки времени идут назад: %v после %v", f.At, lastAt)
		}
		lastAt = f.At
		for _, v := range f.Data[: 1280*720 : 1280*720] {
			if v > 20 {
				nonBlack = true
				break
			}
		}
	}
	elapsed := time.Since(start)
	st := c.Stats()
	t.Logf("%d кадров за %v (%.1f к/с), захвачено новых %d, потерь %d", frames, elapsed, float64(frames)/elapsed.Seconds(), st.Captured, st.Lost)
	if elapsed < time.Second || elapsed > 3*time.Second {
		t.Errorf("45 кадров при 30 к/с должны занять около 1,5 с, заняли %v", elapsed)
	}
	if !nonBlack {
		t.Log("все кадры чёрные: рабочий стол недоступен для дублирования в этом сеансе")
	}
}

func TestCapturerTextureFrames(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	c := openTestCapturer(t, Config{Display: 0, Width: 1280, Height: 720, FPS: 30, WantTexture: true})
	defer c.Close()
	if !c.HasVideoProcessor() {
		t.Skip("видеопроцессор GPU недоступен, текстурный путь не проверить")
	}
	stop := make(chan struct{})
	defer close(stop)
	for i := 0; i < 10; i++ {
		f, err := c.Grab(stop)
		if err != nil {
			t.Fatalf("Grab #%d: %v", i, err)
		}
		if f.Texture == nil {
			t.Fatal("ожидалась текстура NV12")
		}
	}
}

func TestCapturerStop(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	c := openTestCapturer(t, Config{FPS: 5})
	defer c.Close()
	stop := make(chan struct{})
	close(stop)
	if _, err := c.Grab(stop); err != ErrStopped {
		t.Fatalf("после закрытия stop ожидался ErrStopped, получено %v", err)
	}
}

func TestNewUnknownDisplay(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := ListOutputs(); err != nil {
		t.Skipf("перечисление мониторов недоступно: %v", err)
	}
	c, err := New(Config{Display: 99})
	if err == nil {
		c.Close()
		t.Fatal("несуществующий монитор принят")
	}
	if !errors.Is(err, errNoDisplay) {
		t.Fatalf("ожидалась ошибка errNoDisplay, получено %v", err)
	}
	t.Logf("сообщение: %v", err)
}

// Потеря дублирования (как при ACCESS_LOST): выдача кадров продолжается
// повтором последней картинки, а дублирование открывается заново.
func TestCapturerRecoversFromLostDuplication(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	c := openTestCapturer(t, Config{Width: 1280, Height: 720, FPS: 30})
	defer c.Close()
	stop := make(chan struct{})
	defer close(stop)
	for i := 0; i < 5; i++ {
		if _, err := c.Grab(stop); err != nil {
			t.Fatal(err)
		}
	}
	if c.dup == nil {
		t.Skip("дублирование рабочего стола не открылось: проверять восстановление нечего")
	}
	c.dup.Release()
	c.dup = nil
	c.dupRetryAt = time.Time{}
	start := time.Now()
	for i := 0; i < 15; i++ {
		f, err := c.Grab(stop)
		if err != nil {
			t.Fatalf("Grab после потери дублирования: %v", err)
		}
		if len(f.Data) != NV12Size(1280, 720) {
			t.Fatalf("кадр после потери: %d байт", len(f.Data))
		}
	}
	if c.dup == nil {
		t.Fatal("дублирование не восстановлено за 15 кадров")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("15 кадров заняли %v: выдача при потере должна идти с обычной частотой", d)
	}
}

func TestDescribeDupError(t *testing.T) {
	for hr, want := range map[uint32]string{
		hrAccessLost:            "доступ потерян",
		hrAccessDenied:          "защищённый рабочий стол",
		hrNotCurrentlyAvailable: "недоступно",
		hrUnsupported:           "удалённый рабочий стол",
	} {
		if got := describeDupError(&com.Error{Op: "DuplicateOutput", HR: hr}); !strings.Contains(got, want) {
			t.Errorf("HRESULT 0x%08X: %q не содержит %q", hr, got, want)
		}
	}
}
