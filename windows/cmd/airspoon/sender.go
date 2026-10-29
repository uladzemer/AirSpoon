package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"airspoon/internal/airplay"
	"airspoon/internal/capture/dxgi"
	"airspoon/internal/trayctl"
)

// Связка режима трея с протокольным ядром: поиск телевизоров и сам показ.

// mdnsDiscoverer ищет приёмники AirPlay по mDNS; один вызов слушает сеть,
// пока не истечёт ctx (длительность задаёт контроллер).
type mdnsDiscoverer struct {
	mu          sync.Mutex
	lastSummary string
}

func (d *mdnsDiscoverer) Discover(ctx context.Context) ([]trayctl.Device, error) {
	rep := airplay.DiscoverAirPlay(ctx, airplay.DiscoverOptions{LastReceiverPath: defaultLastReceiverFile()})
	d.logIfChanged(rep)
	return devicesFromAdverts(rep.Devices), nil
}

// logIfChanged пишет итог поиска в журнал, только когда он изменился: трей
// ищет каждые несколько секунд, и повторяющиеся строки засорили бы журнал.
func (d *mdnsDiscoverer) logIfChanged(rep *airplay.DiscoveryReport) {
	summary := rep.Summary()
	d.mu.Lock()
	changed := summary != d.lastSummary
	d.lastSummary = summary
	d.mu.Unlock()
	if changed {
		log.Printf("поиск телевизоров: %s", summary)
	}
}

// devicesFromAdverts оставляет приёмники, умеющие показывать экран, и убирает
// повторы (один телевизор отвечает по нескольким сетевым интерфейсам).
func devicesFromAdverts(found []airplay.AirPlayDevice) []trayctl.Device {
	seen := map[string]bool{}
	var out []trayctl.Device
	for i := range found {
		d := found[i]
		if !d.SupportsScreen() || d.IP == "" {
			continue
		}
		addr := net.JoinHostPort(d.IP, strconv.Itoa(d.Port))
		// регистр hex в deviceid у TXT-записи и у /info может различаться
		id := strings.ToLower(d.DeviceID)
		if id == "" {
			id = addr
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		name := d.Name
		if name == "" {
			name = d.IP
		}
		dev := d
		out = append(out, trayctl.Device{ID: id, Name: name, Addr: addr, Data: &dev})
	}
	return out
}

// mirrorStreamer ведёт показ через mirror() — тот же путь, что и у консольной
// команды mirror (проверен на настоящем приёмнике).
type mirrorStreamer struct {
	credFile string
	encoder  string
	// run подменяется в тестах.
	run func(ctx context.Context, o mirrorOptions) error
	// confirmReceiverKeyChange uses the tray's Yes/No dialog; tests replace it.
	confirmReceiverKeyChange func(title, text string) bool
	// onUnsupported вызывается, когда захват экрана не открылся из-за
	// DXGI_ERROR_UNSUPPORTED (экран на другой видеокарте). Возвращает true, если
	// программа сама перезапустится (показ после этого продолжится), иначе false.
	onUnsupported func(t trayctl.Target, err error) bool
}

func newMirrorStreamer() *mirrorStreamer {
	s := &mirrorStreamer{credFile: defaultCredentialsFile(), encoder: "auto", run: wrapTestRun(mirror)}
	s.confirmReceiverKeyChange = confirmReceiverKeyChange
	return s
}

func (s *mirrorStreamer) Stream(ctx context.Context, t trayctl.Target, h trayctl.Hooks) (err error) {
	// Паника внутри показа не должна убивать программу: человек получит обычное
	// сообщение «не получилось», а подробности останутся в журнале.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("авария при показе: %v\n%s", r, debug.Stack())
			err = fmt.Errorf("внутренняя ошибка показа: %v", r)
		}
	}()
	o := mirrorOptions{
		timeout:          3 * time.Second,
		lastReceiverFile: defaultLastReceiverFile(),
		compat:           "auto", // тип приёмника определяется по /info (UxPlay, Apple TV, телевизоры)
		noAudio:          !t.Audio,
		credFile:         s.credFile,
		encoder:          s.encoder, // размер и частота кадров — по холсту приёмника, не выше 1080p60
		pinPrompt:        h.AskPIN,
		passwordPrompt:   h.AskPassword,
		onStreaming:      h.Streaming,
		onNotice:         h.Notice,
		// Запасной путь для поддержки: AIRSPOON_NO_GPU_CONVERT=1 включает
		// преобразование цвета на процессоре (как --no-gpu-convert).
		noGPUConvert: os.Getenv("AIRSPOON_NO_GPU_CONVERT") == "1",
	}
	if dev, ok := t.Device.Data.(*airplay.AirPlayDevice); ok && dev != nil {
		o.advert = dev
	} else {
		o.host = t.Device.Addr
	}
	err = s.runWithReceiverKeyConfirmation(ctx, o)
	if kind := classifyCaptureError(err); kind != nil {
		// Причина известна и перезапуск с другой видеокартой не нужен (HDR, нет
		// Media Foundation, захват недоступен в этом сеансе).
		return fmt.Errorf("%w: %w", kind, err)
	}
	if errors.Is(err, dxgi.ErrDuplicateUnsupported) {
		// Здесь UnsupportedError с Hybrid == true: монитор на другой видеокарте.
		if s.onUnsupported != nil && s.onUnsupported(t, err) {
			return fmt.Errorf("%w: %w", trayctl.ErrRestarting, err)
		}
		return fmt.Errorf("%w: %w", trayctl.ErrCaptureGPU, err)
	}
	return classifyStreamError(err)
}

// classifyStreamError помечает ошибку показа одним из признаков trayctl, по
// которым пользователь получает понятный текст; исходная ошибка остаётся в цепочке.
func classifyStreamError(err error) error {
	if err == nil {
		return nil
	}
	var capture captureError
	var netErr net.Error
	switch {
	case errors.Is(err, trayctl.ErrPINCanceled):
		return err
	case errors.Is(err, airplay.ErrLegacyPINRejected), errors.Is(err, airplay.ErrHAPPINRejected):
		return fmt.Errorf("%w: %w", trayctl.ErrPINRejected, err)
	case errors.As(err, &capture):
		return fmt.Errorf("%w: %w", trayctl.ErrCapture, err)
	case errors.As(err, &netErr), errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: %w", trayctl.ErrUnreachable, err)
	}
	return err
}
