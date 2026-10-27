//go:build windows

package native

import (
	"bytes"
	"context"
	"image"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"airspoon/internal/airplay"
	"airspoon/internal/capture/dxgi"
	"airspoon/internal/encode/mf"
	"airspoon/internal/ffmpegfind"
	"airspoon/internal/win/com"
)

// collect читает access unit'ы из источника заданное время.
func collect(t *testing.T, src airplay.VideoSource, d time.Duration) []airplay.VideoAccessUnit {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var aus []airplay.VideoAccessUnit
	for {
		au, err := src.ReadAccessUnit(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return aus
			}
			t.Fatalf("ReadAccessUnit: %v", err)
		}
		aus = append(aus, au)
	}
}

func hasNALType(data []byte, typ byte) bool {
	for i := 0; i+3 < len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 && data[i+3]&0x1f == typ {
			return true
		}
	}
	return false
}

func TestSourceCaptureEncodeDecode(t *testing.T) {
	ffmpegPath, ffErr := ffmpegfind.Find("")
	for _, mode := range []Mode{ModeHardware, ModeSoftware} {
		t.Run(mode.String(), func(t *testing.T) {
			src, err := Start(context.Background(), Config{
				Mode: mode, FPS: 30, ShowCursor: true,
				Logf: t.Logf, Debugf: t.Logf, StatsInterval: time.Second,
			})
			if err != nil {
				t.Skipf("видеоисточник (%s) не запустился в этом окружении (нет видеокарты или рабочего стола?): %v", mode, err)
			}
			defer src.Close()
			info := src.Info()
			t.Logf("запущено: %+v", info)
			if mode == ModeSoftware && info.Hardware {
				t.Errorf("в режиме sw выбран аппаратный кодер %q", info.Encoder)
			}
			if mode == ModeHardware && !info.Hardware {
				t.Errorf("в режиме hw выбран программный кодер %q", info.Encoder)
			}

			aus := collect(t, src, 2*time.Second)
			st := src.Stats()
			t.Logf("за 2 с: %d кадров, статистика %+v", len(aus), st)
			if st.Captured == 0 {
				t.Skip("рабочий стол недоступен для Desktop Duplication в этом сеансе: проверять нечего")
			}
			if len(aus) < 30 {
				t.Fatalf("за 2 с получено %d кадров, ожидалось около 60", len(aus))
			}
			first := aus[0].AnnexB
			if !hasNALType(first, 7) || !hasNALType(first, 8) || !hasNALType(first, 5) {
				t.Fatal("первый кадр не содержит SPS, PPS и IDR")
			}
			for i := 1; i < len(aus); i++ {
				if aus[i].PTS.Before(aus[i-1].PTS) {
					t.Fatalf("метки времени идут назад на кадре %d", i)
				}
			}
			if st.AvgLatency <= 0 || st.AvgLatency > 500*time.Millisecond {
				t.Errorf("средняя задержка захват->кодер %v вне разумных пределов", st.AvgLatency)
			}

			if ffErr != nil {
				t.Skipf("ffmpeg.exe не найден (%v): декодирование результата пропущено", ffErr)
			}
			var stream bytes.Buffer
			for _, au := range aus {
				stream.Write(au.AnnexB)
			}
			dir := t.TempDir()
			h264 := filepath.Join(dir, "capture.h264")
			if err := os.WriteFile(h264, stream.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(ffmpegPath, "-v", "error", "-y", "-f", "h264", "-i", h264, "-frames:v", "40",
				filepath.Join(dir, "f%03d.png"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("ffmpeg не декодировал поток: %v\n%s", err, out)
			}
			pngs, _ := filepath.Glob(filepath.Join(dir, "f*.png"))
			sort.Strings(pngs)
			if len(pngs) < 20 {
				t.Fatalf("ffmpeg декодировал только %d кадров", len(pngs))
			}
			last := pngs[len(pngs)-1]
			if dump := os.Getenv("AIRSPOON_DUMP_DIR"); dump != "" {
				data, _ := os.ReadFile(last)
				_ = os.WriteFile(filepath.Join(dump, "native_"+mode.String()+".png"), data, 0o644)
				_ = os.WriteFile(filepath.Join(dump, "native_"+mode.String()+".h264"), stream.Bytes(), 0o644)
			}
			f, err := os.Open(last)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			img, _, err := image.Decode(f)
			if err != nil {
				t.Fatal(err)
			}
			if b := img.Bounds(); b.Dx() != 1280 || b.Dy() != 720 {
				t.Fatalf("размер кадра %dx%d, ожидалось 1280x720", b.Dx(), b.Dy())
			}
			bright, total := 0, 0
			var sum, sumSq float64
			b := img.Bounds()
			for y := b.Min.Y; y < b.Max.Y; y += 4 {
				for x := b.Min.X; x < b.Max.X; x += 4 {
					r, g, bl, _ := img.At(x, y).RGBA()
					luma := (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(bl)) / 257
					sum += luma
					sumSq += luma * luma
					total++
					if luma > 40 {
						bright++
					}
				}
			}
			mean := sum / float64(total)
			variance := sumSq/float64(total) - mean*mean
			t.Logf("декодированный кадр: средняя яркость %.1f, дисперсия %.1f, светлых точек %d%%", mean, variance, bright*100/total)
			if bright*100/total < 2 || variance < 25 {
				t.Errorf("кадр пустой или чёрный: яркость %.1f, дисперсия %.1f", mean, variance)
			}
		})
	}
}

func TestSourceCloseIsIdempotentAndUnblocksRead(t *testing.T) {
	src, err := Start(context.Background(), Config{Mode: ModeSoftware, FPS: 30, Logf: t.Logf})
	if err != nil {
		t.Skipf("видеоисточник не запустился в этом окружении: %v", err)
	}
	errc := make(chan error, 1)
	go func() {
		for {
			if _, err := src.ReadAccessUnit(context.Background()); err != nil {
				errc <- err
				return
			}
		}
	}()
	time.Sleep(300 * time.Millisecond)
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errc:
		t.Logf("чтение после Close завершилось: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("ReadAccessUnit не вернулся после Close")
	}
}

func TestStartCancelledContextStopsSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	src, err := Start(ctx, Config{Mode: ModeSoftware, FPS: 30})
	if err != nil {
		t.Skipf("видеоисточник не запустился в этом окружении: %v", err)
	}
	cancel()
	done := make(chan struct{})
	go func() {
		for {
			if _, err := src.ReadAccessUnit(context.Background()); err != nil {
				close(done)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("источник не остановился после отмены контекста")
	}
}

// TestHardwarePathsCompared сравнивает частоту и задержку аппаратного кодера
// при входе текстурами GPU и через системную память (только лог).
func TestHardwarePathsCompared(t *testing.T) {
	for _, noTex := range []bool{false, true} {
		src, err := Start(context.Background(), Config{
			Mode: ModeHardware, FPS: 30, ShowCursor: true, DisableTextures: noTex, Logf: t.Logf,
		})
		if err != nil {
			t.Skipf("аппаратный источник не запустился: %v", err)
		}
		aus := collect(t, src, 3*time.Second)
		st := src.Stats()
		t.Logf("DisableTextures=%t: %d кадров за 3 с, отброшено до кодера %d, задержка avg %v max %v",
			noTex, len(aus), st.Skipped, st.AvgLatency.Round(time.Millisecond), st.MaxLatency.Round(time.Millisecond))
		src.Close()
	}
}

// TestHardwareTextureInput проверяет, что вход аппаратного кодера текстурами GPU
// (IMFDXGIDeviceManager) работает: кадры идут от видеопроцессора прямо в MFT.
// Скорость не проверяется: на части драйверов этот путь медленнее входа через память,
// и Source сам переключается (см. textureMaxSkipPercent).
func TestHardwareTextureInput(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	capt, err := dxgi.New(dxgi.Config{Width: 1280, Height: 720, FPS: 30, WantTexture: true, ShowCursor: true})
	if err != nil {
		t.Skipf("захват экрана недоступен: %v", err)
	}
	defer capt.Close()
	if !capt.HasVideoProcessor() {
		t.Skip("видеопроцессор GPU недоступен")
	}
	var mu sync.Mutex
	var packets []mf.Packet
	enc, err := mf.NewHardware(mf.Config{
		Width: 1280, Height: 720, FPS: 30, BitrateKbps: 2500, Device: capt.Device(),
		Output: func(p mf.Packet) { mu.Lock(); packets = append(packets, p); mu.Unlock() },
	})
	if err != nil {
		t.Skipf("аппаратный кодер недоступен: %v", err)
	}
	defer enc.Close()
	if !enc.Textures() {
		t.Skipf("%s не принимает текстуры D3D11", enc.Name())
	}
	stop := make(chan struct{})
	defer close(stop)
	accepted := 0
	for i := 0; i < 45; i++ {
		f, err := capt.Grab(stop)
		if err != nil {
			t.Fatal(err)
		}
		ok, err := enc.EncodeTexture(f.Texture, int64(i)*10_000_000/30)
		if err != nil {
			t.Fatalf("EncodeTexture #%d: %v", i, err)
		}
		if ok {
			accepted++
		}
	}
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	t.Logf("принято %d из 45 кадров, пакетов %d", accepted, len(packets))
	if accepted < 5 || len(packets) < accepted-3 {
		t.Fatalf("принято %d, получено пакетов %d", accepted, len(packets))
	}
	if !packets[0].Key || !hasNALType(packets[0].AnnexB, 7) || !hasNALType(packets[0].AnnexB, 8) {
		t.Fatal("первый пакет должен быть IDR с SPS и PPS")
	}
}

var procGetProcessHandleCount = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")

// handleCount возвращает число открытых дескрипторов процесса.
func handleCount(t *testing.T) int {
	t.Helper()
	var n uint32
	h, _ := windows.GetCurrentProcess()
	if r, _, err := procGetProcessHandleCount.Call(uintptr(h), uintptr(unsafe.Pointer(&n))); r == 0 {
		t.Fatalf("GetProcessHandleCount: %v", err)
	}
	return int(n)
}

// hasMonitor сообщает, есть ли в этом сеансе монитор, который можно захватить.
// Нет (отключённый удалённый рабочий стол, служба) — тесты с живым захватом
// пропускаются. Есть — любая ошибка запуска источника считается поломкой.
func hasMonitor() bool {
	list, err := dxgi.ListOutputs()
	return err == nil && len(list) > 0
}

// leakSlack — сколько дескрипторов допускается сверх прогрева за измеряемые
// циклы. Замеры (RTX 2080 Ti, NVENC через Media Foundation, режим auto): без захвата и
// кодера рост выходит на плато, с кодером число дескрипторов растёт за первые
// 10-50 циклов (+30 в сумме) и стоит на плато (200 циклов: 439 на 10-м, 458 на
// 50-м, 469 на 200-м, последние 50 циклов +0); рост идёт ступенями по +6...+8. Это
// пул потоков ОС / драйвер (Thread, Event, IoCompletion, IRTimer,
// WaitCompletionPacket), а не наши объекты: число горутин и потоков ОС
// постоянно. Настоящая утечка хотя бы в один дескриптор за цикл даёт +20 за
// 20 измеряемых циклов (у реальных утечек COM-объектов — обычно несколько на
// цикл), выше допуска.
const leakSlack = 12

// TestSourceStartStopDoesNotLeakHandles гоняет циклы «запуск — кадры — остановка»
// и проверяет, что число дескрипторов процесса не растёт от цикла к циклу.
// Программа в трее запускает показ много раз за день.
func TestSourceStartStopDoesNotLeakHandles(t *testing.T) {
	if testing.Short() {
		t.Skip("долгий тест")
	}
	if !hasMonitor() {
		t.Skip("в этом сеансе нет монитора для захвата (рабочий стол недоступен): проверять нечего")
	}
	// Прогрев: пул потоков ОС и драйвер доращивают дескрипторы первые ~30-50 циклов
	// и выходят на плато; проверяем только отсутствие роста на втором отрезке.
	const warmup, cycles = 30, 20
	var before int
	for i := 0; i < warmup+cycles; i++ {
		src, err := Start(context.Background(), Config{Mode: ModeAuto, FPS: 30})
		if err != nil {
			t.Fatalf("цикл %d: видеоисточник не запустился, хотя монитор есть: %v", i+1, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
		for ctx.Err() == nil {
			if _, err := src.ReadAccessUnit(ctx); err != nil {
				break
			}
		}
		cancel()
		if err := src.Close(); err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		time.Sleep(100 * time.Millisecond)
		n := handleCount(t)
		t.Logf("цикл %d: дескрипторов %d", i+1, n)
		if i == warmup-1 {
			before = n
		}
	}
	after := handleCount(t)
	if grow := after - before; grow > leakSlack {
		t.Fatalf("за %d циклов число дескрипторов выросло с %d до %d (+%d, допустимо не более %d): утечка", cycles, before, after, grow, leakSlack)
	}
}
