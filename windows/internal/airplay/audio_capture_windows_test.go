//go:build windows

package airplay

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"
	"unsafe"
)

func TestParseWaveFormatExtensibleFloat(t *testing.T) {
	b := make([]byte, 40)
	binary.LittleEndian.PutUint16(b[0:], waveFormatExtensible)
	binary.LittleEndian.PutUint16(b[2:], 2)
	binary.LittleEndian.PutUint32(b[4:], 48000)
	binary.LittleEndian.PutUint16(b[14:], 32)
	binary.LittleEndian.PutUint16(b[16:], 22)
	binary.LittleEndian.PutUint16(b[18:], 32)
	binary.LittleEndian.PutUint32(b[20:], speakerFrontLeft|speakerFrontRight)
	*(*[16]byte)(b[24:]) = *(*[16]byte)(unsafe.Pointer(&subtypeIEEEFloat))
	got, err := parseWaveFormat(b)
	if err != nil {
		t.Fatal(err)
	}
	want := deviceAudioFormat{Rate: 48000, Channels: 2, BitsPerSample: 32, ValidBits: 32, Float: true, ChannelMask: 3}
	if got != want {
		t.Fatalf("format = %+v, want %+v", got, want)
	}
}

func TestParseWaveFormatPCM16(t *testing.T) {
	b := make([]byte, 18)
	binary.LittleEndian.PutUint16(b[0:], waveFormatPCM)
	binary.LittleEndian.PutUint16(b[2:], 2)
	binary.LittleEndian.PutUint32(b[4:], 44100)
	binary.LittleEndian.PutUint16(b[14:], 16)
	got, err := parseWaveFormat(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Float || got.Rate != 44100 || got.BitsPerSample != 16 {
		t.Fatalf("format = %+v", got)
	}
}

// TestWASAPILoopbackDeliversLiveFrames opens the real default output device.
// Loopback yields no packets while nothing plays, so this also covers the idle
// silence path: frames must flow, contiguous and close to real time.
func TestWASAPILoopbackDeliversLiveFrames(t *testing.T) {
	if err := probeDefaultAudioOutput(); err != nil {
		t.Skipf("no default output device: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ac, err := StartAudioCaptureWithOptions(ctx, AudioCodecALAC, AudioCaptureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer ac.Stop()

	buf := make([]byte, 8192)
	var last audioPCMFramePosition
	start := time.Now()
	const frames = 125 // one second of ALAC
	for i := 0; i < frames; i++ {
		_, position, err := ac.readFramePosition(buf)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		// Loopback stamps packets with their local playout time, slightly ahead
		// of now, and the A/V alignment delays them further.
		if age := time.Since(position.PTS); age < -windowsAudioVideoAlignment-50*time.Millisecond || age > 250*time.Millisecond {
			t.Fatalf("frame %d source age %v, want near the present", i, age)
		}
		if i > 0 && position.SourceRTP-last.SourceRTP != 352 {
			t.Fatalf("frame %d source step %d, want 352", i, position.SourceRTP-last.SourceRTP)
		}
		last = position
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("one second of audio took %v to arrive", elapsed)
	}
}

func TestListAudioDevicesMarksDefault(t *testing.T) {
	if err := probeDefaultAudioOutput(); err != nil {
		t.Skipf("no default output device: %v", err)
	}
	devices, err := ListAudioDevices()
	if err != nil {
		t.Fatal(err)
	}
	defaults := 0
	for _, d := range devices {
		if d.ID == "" {
			t.Fatalf("device without ID: %+v", d)
		}
		if d.Default {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("%d default devices among %d, want exactly 1", defaults, len(devices))
	}
}

type countingSink struct{ idles int }

func (s *countingSink) setFormat(deviceAudioFormat) error   { return nil }
func (s *countingSink) packet([]byte, int, time.Time) error { return nil }
func (s *countingSink) silence(int, time.Time)              {}
func (s *countingSink) idle(time.Time)                      { s.idles++ }

// A chosen device that goes away (unplugged headphones) is opened again once it
// is back, instead of sending silence for the rest of the mirror.
func TestReopenLoopbackReopensAChosenDeviceAfterItFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	attempts := 0
	capture := func(id string) error {
		attempts++
		if id != "headphones" {
			t.Fatalf("captured %q, want the chosen device", id)
		}
		if attempts == 1 {
			return hresultError{op: "start", hr: audclntEDeviceInvalidated}
		}
		cancel() // the second attempt captures until the mirror ends
		return nil
	}
	sink := &countingSink{}
	if err := reopenLoopback(ctx, sink, func() (string, error) { return "headphones", nil }, capture); err != nil {
		t.Fatalf("reopenLoopback = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("capture attempts = %d, want the device reopened (2)", attempts)
	}
	if sink.idles == 0 {
		t.Fatal("no silence was sent while the device was away")
	}
}

// Other errors are retried with a fresh capture each time, and give up after five.
func TestReopenLoopbackRetriesOtherErrorsThenGivesUp(t *testing.T) {
	attempts := 0
	capture := func(string) error {
		attempts++
		return hresultError{op: "start", hr: 0x80004005}
	}
	err := reopenLoopback(context.Background(), &countingSink{}, func() (string, error) { return "speakers", nil }, capture)
	if err == nil || attempts != 5 {
		t.Fatalf("reopenLoopback = %v after %d attempts, want an error after 5", err, attempts)
	}
}

// fastReopen сокращает паузы между попытками, чтобы тесты не ждали полсекунды.
func fastReopen(t *testing.T) {
	t.Helper()
	old := wasapiReopenDelay
	wasapiReopenDelay = 2 * time.Millisecond
	t.Cleanup(func() { wasapiReopenDelay = old })
}

func notFoundErr() error { return hresultError{op: "get default output device", hr: hresultENotFound} }

func TestIsNoAudioDevice(t *testing.T) {
	if !isNoAudioDevice(notFoundErr()) || !isNoAudioDevice(errNoAudioOutput) {
		t.Fatal("E_NOTFOUND and errNoAudioOutput must count as no device")
	}
	wrapped := fmt.Errorf("probe: %w", errNoAudioOutput)
	if !isNoAudioDevice(wrapped) {
		t.Fatal("wrapped errNoAudioOutput must count as no device")
	}
	if isNoAudioDevice(hresultError{op: "start", hr: 0x80004005}) || isNoAudioDevice(nil) {
		t.Fatal("other errors and nil are not a missing device")
	}
}

// Устройства нет дольше, чем лимит в 5 сбоев: захват не сдаётся, шлёт тишину,
// а когда устройство появилось, открывает его.
func TestReopenLoopbackWaitsForDeviceBeyondTheFailureLimit(t *testing.T) {
	fastReopen(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const missing = 12
	resolves, captures := 0, 0
	resolve := func() (string, error) {
		resolves++
		if resolves <= missing {
			return "", notFoundErr()
		}
		return "speakers", nil
	}
	capture := func(id string) error {
		captures++
		if id != "speakers" {
			t.Fatalf("captured %q, want the device that appeared", id)
		}
		cancel()
		return nil
	}
	sink := &countingSink{}
	if err := reopenLoopback(ctx, sink, resolve, capture); err != nil {
		t.Fatalf("reopenLoopback = %v, want to keep waiting for the device", err)
	}
	if resolves != missing+1 || captures != 1 {
		t.Fatalf("resolves=%d captures=%d, want %d and 1", resolves, captures, missing+1)
	}
	if sink.idles == 0 {
		t.Fatal("no silence was sent while there was no device")
	}
}

// Устройство исчезло уже во время захвата (E_NOTFOUND из capture), и это тоже не сбой.
func TestReopenLoopbackNotFoundFromCaptureIsNotAFailure(t *testing.T) {
	fastReopen(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	attempts := 0
	capture := func(string) error {
		attempts++
		if attempts <= 9 {
			return notFoundErr()
		}
		cancel()
		return nil
	}
	if err := reopenLoopback(ctx, &countingSink{}, func() (string, error) { return "x", nil }, capture); err != nil || attempts != 10 {
		t.Fatalf("reopenLoopback = %v after %d attempts, want nil after 10", err, attempts)
	}
}

// Сбои, разделённые долгой нормальной работой, не складываются в серию из пяти.
func TestReopenLoopbackHealthyRunResetsTheFailureCount(t *testing.T) {
	fastReopen(t)
	old := wasapiHealthyRun
	wasapiHealthyRun = 20 * time.Millisecond
	t.Cleanup(func() { wasapiHealthyRun = old })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	attempts := 0
	capture := func(string) error {
		attempts++
		if attempts > 8 {
			cancel()
			return nil
		}
		time.Sleep(30 * time.Millisecond) // дольше wasapiHealthyRun
		return hresultError{op: "read", hr: 0x80004005}
	}
	if err := reopenLoopback(ctx, &countingSink{}, func() (string, error) { return "x", nil }, capture); err != nil {
		t.Fatalf("reopenLoopback = %v, want failures spread over long runs not to add up", err)
	}
}

// Смена устройства по умолчанию: захват переходит на новое сразу, без пауз и без счёта сбоев.
func TestReopenLoopbackFollowsDefaultDeviceChanges(t *testing.T) {
	fastReopen(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	devices := []string{"speakers", "headphones", "hdmi", "speakers", "headphones", "hdmi", "speakers"}
	current := 0
	var captured []string
	resolve := func() (string, error) { return devices[current], nil }
	capture := func(id string) error {
		captured = append(captured, id)
		if current == len(devices)-1 {
			cancel()
			return nil
		}
		current++ // пользователь переключил устройство по умолчанию
		return errAudioDeviceChanged
	}
	sink := &countingSink{}
	if err := reopenLoopback(ctx, sink, resolve, capture); err != nil {
		t.Fatalf("reopenLoopback = %v", err)
	}
	if fmt.Sprint(captured) != fmt.Sprint(devices) {
		t.Fatalf("captured %v, want it to follow %v", captured, devices)
	}
	if sink.idles != 0 {
		t.Fatalf("%d idle ticks while switching devices, want an immediate switch", sink.idles)
	}
}

// С нуля без устройства: насос выдаёт кадры тишины, потом устройство появляется и
// звук идёт дальше тем же потоком кадров.
func TestPumpKeepsFramesFlowingUntilTheDeviceAppears(t *testing.T) {
	fastReopen(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pump := newPCMFramePump(352, 0)
	appeared := time.Now().Add(300 * time.Millisecond)
	resolve := func() (string, error) {
		if time.Now().Before(appeared) {
			return "", notFoundErr()
		}
		return "speakers", nil
	}
	formatSet := make(chan struct{})
	capture := func(string) error {
		if err := pump.setFormat(deviceAudioFormat{Rate: 48000, Channels: 2, BitsPerSample: 16}); err != nil {
			return err
		}
		close(formatSet)
		packet := make([]byte, 480*4)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case now := <-ticker.C:
				if err := pump.packet(packet, 480, now.Add(-10*time.Millisecond)); err != nil {
					return err
				}
			}
		}
	}
	done := make(chan error, 1)
	go func() { done <- reopenLoopback(ctx, pump, resolve, capture) }()

	buf := make([]byte, 352*4)
	var last audioPCMFramePosition
	sawFormat := false
	readDeadline := time.After(5 * time.Second)
	for i := 0; i < 150; i++ {
		type result struct {
			pos audioPCMFramePosition
			err error
		}
		ch := make(chan result, 1)
		go func() {
			pos, err := pump.ReadPCMFramePosition(buf)
			ch <- result{pos, err}
		}()
		select {
		case r := <-ch:
			if r.err != nil {
				t.Fatalf("frame %d: %v", i, r.err)
			}
			if i > 0 && r.pos.SourceRTP <= last.SourceRTP {
				t.Fatalf("frame %d source %d does not advance past %d", i, r.pos.SourceRTP, last.SourceRTP)
			}
			last = r.pos
		case <-readDeadline:
			t.Fatalf("frame %d never arrived (format set: %v)", i, sawFormat)
		}
		select {
		case <-formatSet:
			sawFormat = true
		default:
		}
	}
	if !sawFormat {
		t.Fatal("the device never appeared during the read")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("reopenLoopback = %v", err)
	}
}

// Временные сбои (устройство занято, служба звука перезапускается) не упираются
// в лимит из пяти попыток: звук возвращается, когда причина пропала.
func TestReopenLoopbackTransientErrorsHaveNoRetryLimit(t *testing.T) {
	fastReopen(t)
	for _, hr := range []uint32{audclntEDeviceInUse, audclntEServiceNotRunning} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		attempts := 0
		capture := func(string) error {
			attempts++
			if attempts <= 12 {
				return hresultError{op: "initialize", hr: hr}
			}
			cancel()
			return nil
		}
		err := reopenLoopback(ctx, &countingSink{}, func() (string, error) { return "x", nil }, capture)
		cancel()
		if err != nil || attempts != 13 {
			t.Fatalf("hr 0x%08X: reopenLoopback = %v after %d attempts, want nil after 13", hr, err, attempts)
		}
	}
}

// Пауза между временными сбоями растёт и упирается в потолок.
func TestTransientDelayGrowsToCeiling(t *testing.T) {
	if got := transientDelay(1); got != wasapiReopenDelay {
		t.Fatalf("transientDelay(1) = %v, want %v", got, wasapiReopenDelay)
	}
	if got := transientDelay(2); got != 2*wasapiReopenDelay {
		t.Fatalf("transientDelay(2) = %v, want %v", got, 2*wasapiReopenDelay)
	}
	if got := transientDelay(100); got != wasapiTransientMaxDelay {
		t.Fatalf("transientDelay(100) = %v, want %v", got, wasapiTransientMaxDelay)
	}
}
