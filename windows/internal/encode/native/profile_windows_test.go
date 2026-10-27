//go:build windows

package native

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"airspoon/internal/encode/ffmpeg"
	"airspoon/internal/encode/mf"
)

func TestInfoClass(t *testing.T) {
	if (Info{Hardware: true}).Class() != ClassHardware || (Info{}).Class() != ClassSoftware {
		t.Fatal("Info.Class не соответствует Info.Hardware")
	}
	if ClassHardware.String() != "аппаратный" || ClassSoftware.String() != "программный" {
		t.Fatal("названия классов")
	}
}

func TestApplyProfileRecomputesAutoBitrate(t *testing.T) {
	cfg := Config{Width: 1920, Height: 1080, FPS: 60, BitrateKbps: ffmpeg.AutoBitrateKbps(1920, 1080, 60)}
	out, changed := applyProfile(cfg, true, Profile{Width: 1280, Height: 720, FPS: 30})
	if !changed || out.Width != 1280 || out.Height != 720 || out.FPS != 30 {
		t.Fatalf("профиль не применён: %+v", out)
	}
	if want := ffmpeg.AutoBitrateKbps(1280, 720, 30); out.BitrateKbps != want {
		t.Fatalf("битрейт %d, ожидали пересчёт под 720p30: %d", out.BitrateKbps, want)
	}
	if out.BitrateKbps >= cfg.BitrateKbps {
		t.Fatalf("битрейт не уменьшился: %d -> %d", cfg.BitrateKbps, out.BitrateKbps)
	}
}

func TestApplyProfileKeepsUserBitrate(t *testing.T) {
	cfg := Config{Width: 1920, Height: 1080, FPS: 60, BitrateKbps: 12345}
	out, changed := applyProfile(cfg, false, Profile{Width: 1280, Height: 720, FPS: 30})
	if !changed || out.BitrateKbps != 12345 {
		t.Fatalf("заданный пользователем битрейт изменён: %+v", out)
	}
	out, _ = applyProfile(cfg, false, Profile{FPS: 30, BitrateKbps: 4000})
	if out.BitrateKbps != 4000 || out.Width != 1920 {
		t.Fatalf("явный битрейт профиля не применён или размер сбит: %+v", out)
	}
}

func TestApplyProfileNoChange(t *testing.T) {
	cfg := Config{Width: 1280, Height: 720, FPS: 30, BitrateKbps: 2500}
	if _, changed := applyProfile(cfg, true, Profile{}); changed {
		t.Fatal("пустой профиль считается изменением")
	}
	if _, changed := applyProfile(cfg, true, profileOf(cfg)); changed {
		t.Fatal("тот же профиль считается изменением")
	}
	out, _ := applyProfile(cfg, true, Profile{Width: 641, Height: 361})
	if out.Width != 640 || out.Height != 360 {
		t.Fatalf("нечётный размер не выровнен: %dx%d", out.Width, out.Height)
	}
}

func TestStatsProblem(t *testing.T) {
	healthy := statsWindow{Dur: 10 * time.Second, Offered: 300, Skipped: 20, AvgLatency: 30 * time.Millisecond}
	if msg := statsProblem(healthy); msg != "" {
		t.Fatalf("здоровое окно (6%% пропусков) даёт сообщение: %q", msg)
	}
	cases := []struct {
		name string
		w    statsWindow
		want string
	}{
		{"пропуски", statsWindow{Dur: 10 * time.Second, Offered: 300, Skipped: 31}, "пропущено 31 кадров из 300 (10%)"},
		{"сброшенные пакеты", statsWindow{Dur: 10 * time.Second, Offered: 300, Dropped: 4}, "сброшено пакетов 4"},
		{"задержка", statsWindow{Dur: 10 * time.Second, Offered: 300, AvgLatency: 150 * time.Millisecond}, "150 мс"},
	}
	for _, c := range cases {
		msg := statsProblem(c.w)
		if !strings.HasPrefix(msg, "[ВИДЕО] проблемы за 10 с: ") || !strings.Contains(msg, c.want) {
			t.Errorf("%s: %q не содержит %q", c.name, msg, c.want)
		}
	}
	// Ровно 10% — ещё не проблема, больше — проблема.
	if statsProblem(statsWindow{Offered: 100, Skipped: 10}) != "" {
		t.Error("ровно 10% пропусков принято за проблему")
	}
	if statsProblem(statsWindow{Offered: 100, Skipped: 11}) == "" {
		t.Error("11% пропусков не замечено")
	}
	// Мало кадров — процент не считаем.
	if statsProblem(statsWindow{Offered: 5, Skipped: 5}) != "" {
		t.Error("процент пропусков посчитан по 5 кадрам")
	}
	// Все три причины в одной строке.
	all := statsProblem(statsWindow{Dur: 10 * time.Second, Offered: 100, Skipped: 50, Dropped: 1, AvgLatency: time.Second})
	if strings.Count(all, "; ") != 2 {
		t.Errorf("ожидали три причины через «; »: %q", all)
	}
}

// Живой источник: ChooseProfile видит класс кодера и может перезапустить
// источник с меньшим профилем; размер и частота в Info меняются.
func TestChooseProfileRestartsSource(t *testing.T) {
	if !hasMonitor() {
		t.Skip("в этом сеансе нет монитора для захвата (рабочий стол недоступен): проверять нечего")
	}
	var mu sync.Mutex
	var seen []Info
	var calls atomic.Int32
	var logs []string
	src, err := Start(context.Background(), Config{
		Mode: ModeSoftware, Width: 1280, Height: 720, FPS: 30,
		Logf: func(f string, a ...any) {
			mu.Lock()
			logs = append(logs, strings.TrimSpace(fmt.Sprintf(f, a...)))
			mu.Unlock()
		},
		ChooseProfile: func(i Info) (Profile, bool) {
			calls.Add(1)
			mu.Lock()
			seen = append(seen, i)
			mu.Unlock()
			return Profile{Width: 640, Height: 360, FPS: 15}, true
		},
	})
	if err != nil {
		t.Skipf("видеоисточник не запустился в этом окружении: %v", err)
	}
	defer src.Close()
	if n := calls.Load(); n != 1 {
		t.Fatalf("ChooseProfile вызван %d раз, ожидали 1", n)
	}
	if len(seen) != 1 || seen[0].Class() != ClassSoftware || seen[0].Width != 1280 || seen[0].FPS != 30 {
		t.Fatalf("в ChooseProfile пришло %+v", seen)
	}
	info := src.Info()
	if info.Width != 640 || info.Height != 360 || info.FPS != 15 {
		t.Fatalf("после смены профиля в Info %dx%d@%d", info.Width, info.Height, info.FPS)
	}
	if want := ffmpeg.AutoBitrateKbps(640, 360, 15); info.BitrateKbps != want {
		t.Errorf("битрейт %d, ожидали пересчёт под новый профиль: %d", info.BitrateKbps, want)
	}
	aus := collect(t, src, 1500*time.Millisecond)
	if st := src.Stats(); st.Captured == 0 {
		t.Skip("рабочий стол недоступен для Desktop Duplication в этом сеансе")
	}
	if len(aus) < 5 {
		t.Fatalf("после перезапуска источник выдал %d кадров", len(aus))
	}
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, l := range logs {
		found = found || strings.Contains(l, "профиль меняется")
	}
	if !found {
		t.Errorf("в журнале нет строки о смене профиля: %v", logs)
	}
}

// Находка 3: после ухода с аппаратного кодера по цепочке запасных ChooseProfile
// вызывается снова и, если профиль меняется, источник поднимается заново без
// аппаратных вариантов. Сбой аппаратного кодера подставляется faultHook.
func TestChooseProfileAgainAfterFallbackToSoftware(t *testing.T) {
	if !hasMonitor() {
		t.Skip("в этом сеансе нет монитора для захвата (рабочий стол недоступен): проверять нечего")
	}
	var mu sync.Mutex
	var classes []EncoderClass
	var logs []string
	var faults atomic.Int32
	src, err := Start(context.Background(), Config{
		Mode: ModeAuto, Width: 1280, Height: 720, FPS: 30,
		Logf: func(f string, a ...any) {
			mu.Lock()
			logs = append(logs, strings.TrimSpace(fmt.Sprintf(f, a...)))
			mu.Unlock()
		},
		ChooseProfile: func(i Info) (Profile, bool) {
			mu.Lock()
			classes = append(classes, i.Class())
			mu.Unlock()
			if i.Class() == ClassHardware {
				return Profile{}, false
			}
			return Profile{Width: 640, Height: 360, FPS: 15}, true
		},
		faultHook: func(enc *mf.Encoder) error {
			if enc.Hardware() {
				faults.Add(1)
				return errors.New("подставной сбой аппаратного кодера")
			}
			return nil
		},
	})
	if err != nil {
		t.Skipf("видеоисточник не запустился в этом окружении: %v", err)
	}
	defer src.Close()
	if src.Info().Class() != ClassHardware {
		t.Skip("аппаратного кодера нет: переход аппаратный -> программный проверять нечем")
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && src.Info().Class() != ClassSoftware {
		time.Sleep(50 * time.Millisecond)
	}
	if faults.Load() == 0 {
		t.Skip("рабочий стол не отдаёт кадры в этом сеансе: сбой не подставился")
	}
	info := src.Info()
	if info.Class() != ClassSoftware || info.Width != 640 || info.Height != 360 || info.FPS != 15 {
		t.Fatalf("после перехода на программный кодер Info %+v", info)
	}
	mu.Lock()
	got := append([]EncoderClass(nil), classes...)
	logged := strings.Join(logs, "\n")
	mu.Unlock()
	if len(got) != 2 || got[0] != ClassHardware || got[1] != ClassSoftware {
		t.Errorf("ChooseProfile вызван с классами %v, ожидали [аппаратный программный]", got)
	}
	if !strings.Contains(logged, "профиль меняется") {
		t.Errorf("в журнале нет строки о смене профиля:\n%s", logged)
	}
	// Источник после перезапуска живой и не вернулся к аппаратному кодеру.
	aus := collect(t, src, 1500*time.Millisecond)
	if len(aus) < 3 {
		t.Fatalf("после перезапуска источник выдал %d кадров", len(aus))
	}
	if src.Info().Class() != ClassSoftware {
		t.Errorf("источник вернулся к аппаратному кодеру: %+v", src.Info())
	}
}

// Цепочка кодеров после ухода с аппаратного не содержит аппаратных вариантов.
func TestChainSoftOnly(t *testing.T) {
	s := &Source{cfg: Config{Mode: ModeAuto}}
	if c := s.chain(true); len(c) != 3 || !c[0].hardware {
		t.Fatalf("обычная цепочка: %v", c)
	}
	s.softOnly = true
	if c := s.chain(true); len(c) != 1 || c[0].hardware {
		t.Fatalf("цепочка softOnly: %v", c)
	}
}

type fakeConvertPath struct {
	gpu    bool
	reason string
}

func (f *fakeConvertPath) ConvertPath() (bool, string) { return f.gpu, f.reason }

// Находка 7: Info и журнал следуют за путём преобразования цвета, когда
// видеопроцессор GPU отключается посреди показа; без изменений — тишина.
func TestSyncConvertPathUpdatesInfo(t *testing.T) {
	var logs []string
	s := &Source{cfg: Config{Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }}}
	s.info.Store(&Info{Encoder: "enc", Hardware: true, GPUConvert: true, Width: 1920, Height: 1080, FPS: 60})
	fc := &fakeConvertPath{gpu: true}
	s.syncConvertPath(fc)
	if len(logs) != 0 || !s.Info().GPUConvert {
		t.Fatalf("без изменений Info/журнал тронуты: %+v %v", s.Info(), logs)
	}
	fc.gpu, fc.reason = false, "ошибка VideoProcessorBlt: 0x887a0005"
	s.syncConvertPath(fc)
	if s.Info().GPUConvert {
		t.Fatal("Info.GPUConvert остался true после перехода на CPU")
	}
	if i := s.Info(); i.Width != 1920 || i.FPS != 60 || i.Encoder != "enc" {
		t.Errorf("прочие поля Info испорчены: %+v", i)
	}
	all := strings.Join(logs, "\n")
	if !strings.Contains(all, "0x887a0005") || !strings.Contains(all, "преобразование цвета на CPU") {
		t.Errorf("в журнале нет причины или нового пути:\n%s", all)
	}
	n := len(logs)
	s.syncConvertPath(fc)
	if len(logs) != n {
		t.Error("повторный вызов без изменений снова пишет в журнал")
	}
}
