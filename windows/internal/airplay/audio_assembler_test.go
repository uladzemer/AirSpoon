package airplay

import (
	"encoding/binary"
	"math"
	"testing"
	"time"
)

func stereoRamp(frames int, start float32) []float32 {
	out := make([]float32, frames*2)
	for i := 0; i < frames; i++ {
		v := start + float32(i)/32768
		out[2*i] = v
		out[2*i+1] = -v
	}
	return out
}

func collectFrames(a *pcmFrameAssembler) []capturedPCMFrame {
	var frames []capturedPCMFrame
	for {
		f, ok := a.popFrame()
		if !ok {
			return frames
		}
		frames = append(frames, f)
	}
}

func TestAssemblerEmitsExactCodecFramesFromArbitraryPackets(t *testing.T) {
	base := time.Unix(1000, 0)
	a := newPCMFrameAssembler(352)
	a.setInputRate(44100)
	// 1000 frames in odd-sized packets: 2 whole 352-sample frames, 296 left over.
	written := 0
	for _, n := range []int{100, 333, 7, 560} {
		a.writeStereo(stereoRamp(n, float32(written)/32768), base.Add(audioSamplesDuration(uint64(written))))
		written += n
	}
	frames := collectFrames(a)
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(frames))
	}
	for i, f := range frames {
		if len(f.pcm) != 352*4 {
			t.Fatalf("frame %d is %d bytes, want 1408", i, len(f.pcm))
		}
		if f.position.SourceRTP != uint32(i*352) || !f.position.HasSourceRTP {
			t.Fatalf("frame %d source position = %+v, want %d", i, f.position, i*352)
		}
		wantPTS := base.Add(audioSamplesDuration(uint64(i * 352)))
		if d := f.position.PTS.Sub(wantPTS); d < -time.Microsecond || d > time.Microsecond {
			t.Fatalf("frame %d PTS off by %v", i, d)
		}
	}
	// At 44.1 kHz the samples pass through unchanged: sample k of the ramp is k.
	second := frames[1].pcm
	left := int16(binary.LittleEndian.Uint16(second[0:]))
	right := int16(binary.LittleEndian.Uint16(second[2:]))
	if left != 352 || right != -352 {
		t.Fatalf("first sample of frame 1 = (%d,%d), want (352,-352)", left, right)
	}
}

func TestAssemblerFillsRealGapsWithSilenceAndKeepsTimeline(t *testing.T) {
	base := time.Unix(2000, 0)
	a := newPCMFrameAssembler(352)
	a.setInputRate(44100)
	a.writeStereo(stereoRamp(352, 0.5), base)
	// Следующий пакет приходит через 60 мс (2646 отсчётов) после конца первого:
	// это настоящий разрыв больше assemblerResyncThreshold.
	gapStart := base.Add(audioSamplesDuration(352))
	a.writeStereo(stereoRamp(1000, 0.5), gapStart.Add(60*time.Millisecond))
	frames := collectFrames(a)
	// 352 + 2646 тишины + 1000 = 3998 отсчётов -> 11 целых кадров.
	if len(frames) != 11 {
		t.Fatalf("frames = %d, want 11", len(frames))
	}
	for i, f := range frames {
		if f.position.SourceRTP != uint32(i*352) {
			t.Fatalf("frame %d source position %d, want contiguous %d", i, f.position.SourceRTP, i*352)
		}
	}
	// Кадр 1 целиком внутри разрыва, значит тихий.
	for i := 0; i < 352*4; i++ {
		if frames[1].pcm[i] != 0 {
			t.Fatalf("frame 1 byte %d = %d, want silence", i, frames[1].pcm[i])
		}
	}
	if st := a.takeStats(); st.gapFills != 1 || st.gapFillSamples != 2646 {
		t.Fatalf("stats = %+v, want one gap fill of 2646 samples", st)
	}
}

func TestAssemblerTrimsRealOverlapWithAlreadyCapturedTimeline(t *testing.T) {
	base := time.Unix(3000, 0)
	a := newPCMFrameAssembler(352)
	a.setInputRate(44100)
	a.fillSilenceUntil(base.Add(100*time.Millisecond), base) // 4410 отсчётов тишины
	// Настоящие данные начинаются на 50 мс (2205 отсчётов) раньше конца уже
	// выданной тишины: это перекрытие больше порога, его нужно обрезать.
	start := base.Add(50 * time.Millisecond)
	a.writeStereo(stereoRamp(4410, 0.25), start)
	frames := collectFrames(a)
	// 4410 + (4410 - 2205) = 6615 отсчётов -> 18 целых кадров.
	if len(frames) != 18 {
		t.Fatalf("frames = %d, want 18 (overlap must not duplicate samples)", len(frames))
	}
	// Отсчёт 4410 потока — отсчёт 2205 пакета; он лежит в кадре 12 со смещением 186.
	got := int16(binary.LittleEndian.Uint16(frames[12].pcm[186*4:]))
	want := int16(math.Round(float64(0.25+2205.0/32768) * 32767))
	if got < want-1 || got > want+1 {
		t.Fatalf("first kept sample = %d, want ~%d (packet sample 2205)", got, want)
	}
	if st := a.takeStats(); st.trims != 1 || st.trimSamples != 2205 {
		t.Fatalf("stats = %+v, want one trim of 2205 samples", st)
	}
}

func TestAssemblerLongGapRestartsTimelineWithSourceGap(t *testing.T) {
	base := time.Unix(4000, 0)
	a := newPCMFrameAssembler(352)
	a.setInputRate(44100)
	a.writeStereo(stereoRamp(352, 0), base)
	resume := base.Add(5 * time.Second)
	a.writeStereo(stereoRamp(352, 0), resume)
	frames := collectFrames(a)
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(frames))
	}
	gap := frames[1].position.SourceRTP - frames[0].position.SourceRTP
	if want := uint32(5 * 44100); gap != want {
		t.Fatalf("source gap = %d samples, want %d", gap, want)
	}
	if d := frames[1].position.PTS.Sub(resume); d < -time.Microsecond || d > time.Microsecond {
		t.Fatalf("resumed frame PTS off by %v", d)
	}
}

func TestAssemblerIdleSilenceKeepsFramesFlowing(t *testing.T) {
	base := time.Unix(5000, 0)
	a := newPCMFrameAssembler(480)
	a.setInputRate(48000)
	a.fillSilenceUntil(base.Add(100*time.Millisecond), base)
	frames := collectFrames(a)
	// 100 ms at 44.1 kHz = 4410 samples; the resampler holds back a few samples
	// of look-ahead, so at least 9 frames of 480 must be out.
	if len(frames) < 8 || len(frames) > 9 {
		t.Fatalf("frames = %d, want 8 or 9", len(frames))
	}
	for i, f := range frames {
		wantPTS := base.Add(audioSamplesDuration(uint64(i * 480)))
		if d := f.position.PTS.Sub(wantPTS); d < -50*time.Microsecond || d > 50*time.Microsecond {
			t.Fatalf("frame %d PTS off by %v", i, d)
		}
	}
}

func TestAssemblerRateChangeKeepsSourcePositionsMonotonic(t *testing.T) {
	base := time.Unix(6000, 0)
	a := newPCMFrameAssembler(352)
	a.setInputRate(48000)
	a.writeStereo(make([]float32, 2*4800), base)
	next := base.Add(100 * time.Millisecond)
	a.setInputRate(44100)
	a.writeStereo(make([]float32, 2*4410), next)
	frames := collectFrames(a)
	if len(frames) < 24 {
		t.Fatalf("frames = %d, want about 25", len(frames))
	}
	for i := 1; i < len(frames); i++ {
		if frames[i].position.SourceRTP-frames[i-1].position.SourceRTP != 352 {
			t.Fatalf("frame %d source step %d, want 352", i, frames[i].position.SourceRTP-frames[i-1].position.SourceRTP)
		}
		if !frames[i].position.PTS.After(frames[i-1].position.PTS) {
			t.Fatalf("frame %d PTS not increasing", i)
		}
	}
}
