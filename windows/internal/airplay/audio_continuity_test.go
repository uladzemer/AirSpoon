package airplay

import (
	"encoding/binary"
	"math"
	"math/rand"
	"testing"
	"time"
)

// Стенд непрерывности звука: синус 997 Гц отдаётся в pcmFramePump так, как его
// отдаёт опрос WASAPI (пакеты по 10 мс, опрос каждые 5 мс), с запаздыванием
// доставки, дрожью PTS, грубым тиком таймера Windows и паузами. Любая вставка
// тишины или обрезка сборщика в живом звуке — это щелчок или обрыв, поэтому
// в «чистых» сценариях их не должно быть вовсе.

const (
	contFreq       = 997.0 // не кратна 1 кГц: отсчёт с точным нулём бывает только первым
	contAmp        = 0.5
	contPollStep   = 5 * time.Millisecond
	contPacketTime = 10 * time.Millisecond
	// contQPCOffset — произвольное смещение QPC относительно времени стенда.
	contQPCOffset = uint64(7_000_000_000)
)

type continuitySim struct {
	rate   int
	float  bool
	dur    time.Duration
	seed   int64
	delay  func(j int, rng *rand.Rand) time.Duration // запаздывание доставки после конца пакета
	ptsJit time.Duration                             // ±дрожь PTS пакета
	// tick > 0: PTS берутся из qpcClock, собранного на виртуальных часах с
	// грубым монотонным отсчётом time.Now (как тик 15,6 мс в Windows), якорь
	// обновляется раз в секунду, как в captureLoopback.
	tick time.Duration
	// Пакеты, чей старт попал в [pauseFrom, pauseTo), не доставляются (пауза звука).
	pauseFrom, pauseTo time.Duration
}

type continuityResult struct {
	frames      []capturedPCMFrame
	pcm         []byte // выход конвейера, S16LE стерео 44,1 кГц
	stats       assemblerStats
	inFrames    int           // отсчётов устройства подано
	maxClockErr time.Duration // наибольшая ошибка перевода QPC во время (только с tick)
}

func (c continuitySim) run() continuityResult {
	rng := rand.New(rand.NewSource(c.seed))
	base := time.Unix(1_000_000, 0)
	pump := newPCMFramePump(352, 0)
	format := deviceAudioFormat{Rate: c.rate, Channels: 2, BitsPerSample: 16, ChannelMask: 3}
	bytesPerSample := 2
	if c.float {
		format = deviceAudioFormat{Rate: c.rate, Channels: 2, BitsPerSample: 32, Float: true, ChannelMask: 3}
		bytesPerSample = 4
	}
	if err := pump.setFormat(format); err != nil {
		panic(err)
	}

	// Виртуальные часы: cur — истинное время с начала стенда.
	var cur time.Duration
	var clk *qpcClock
	if c.tick > 0 {
		clk = newQPCClockFrom(qpcClockSources{
			qpc: func() uint64 { cur += time.Microsecond; return uint64(cur/100) + contQPCOffset },
			precise: func() (time.Duration, bool) {
				cur += time.Microsecond
				return cur + 5000*time.Second, true
			},
			now: func() time.Time {
				cur += 2 * time.Microsecond
				return base.Add(cur - cur%c.tick)
			},
		})
	}

	pktFrames := c.rate / 100
	nPk := int(c.dur / contPacketTime)
	type packet struct {
		data  []byte
		pts   time.Time
		avail time.Duration
	}
	pkts := make([]packet, 0, nPk)
	var prevAvail time.Duration
	for j := 0; j < nPk; j++ {
		n0 := j * pktFrames
		start := samplesDurationAt(uint64(n0), c.rate)
		if start >= c.pauseFrom && start < c.pauseTo {
			continue
		}
		data := make([]byte, pktFrames*2*bytesPerSample)
		for i := 0; i < pktFrames; i++ {
			v := contAmp * math.Sin(2*math.Pi*contFreq*float64(n0+i)/float64(c.rate))
			if c.float {
				binary.LittleEndian.PutUint32(data[8*i:], math.Float32bits(float32(v)))
				binary.LittleEndian.PutUint32(data[8*i+4:], math.Float32bits(float32(v)))
			} else {
				s := uint16(int16(math.Round(v * 32767)))
				binary.LittleEndian.PutUint16(data[4*i:], s)
				binary.LittleEndian.PutUint16(data[4*i+2:], s)
			}
		}
		avail := samplesDurationAt(uint64(n0+pktFrames), c.rate) + c.delay(j, rng)
		if avail < prevAvail {
			avail = prevAvail
		}
		prevAvail = avail
		pts := base.Add(start)
		if clk != nil {
			pts = clk.timeAt(uint64(start/100) + contQPCOffset)
		}
		if c.ptsJit > 0 {
			pts = pts.Add(time.Duration((rng.Float64()*2 - 1) * float64(c.ptsJit)))
		}
		pkts = append(pkts, packet{data, pts, avail})
	}

	var res continuityResult
	next := 0
	lastReanchor := time.Duration(0)
	for poll := contPollStep; poll < c.dur+30*time.Millisecond; poll += contPollStep {
		cur = poll
		now := base.Add(poll)
		if clk != nil {
			if poll-lastReanchor >= time.Second {
				clk.reanchor()
				lastReanchor = poll
			}
			if e := clk.timeAt(uint64(poll/100) + contQPCOffset).Sub(base.Add(poll)); max(e, -e) > res.maxClockErr {
				res.maxClockErr = max(e, -e)
			}
			now = clk.nowTime()
		}
		got := false
		for next < len(pkts) && pkts[next].avail <= poll {
			if err := pump.packet(pkts[next].data, pktFrames, pkts[next].pts); err != nil {
				panic(err)
			}
			res.inFrames += pktFrames
			next++
			got = true
		}
		if !got {
			pump.idle(now)
		}
		for len(pump.frames) > 0 {
			res.frames = append(res.frames, <-pump.frames)
		}
	}
	for _, f := range res.frames {
		res.pcm = append(res.pcm, f.pcm...)
	}
	res.stats = pump.takeAssemblerStats()
	return res
}

// defects считает в выходе дыры (две подряд нулевые пары отсчётов) и скачки
// ≥ 8192 LSB; у синуса 997 Гц амплитуды 0,5 естественный шаг около 2300.
func (r continuityResult) defects() (holes, bigSteps int) {
	n := len(r.pcm) / 4
	run := 0
	for i := 1; i < n; i++ {
		l := int(int16(binary.LittleEndian.Uint16(r.pcm[4*i:])))
		pl := int(int16(binary.LittleEndian.Uint16(r.pcm[4*i-4:])))
		if l == 0 {
			run++
			if run == 2 {
				holes++
			}
		} else {
			run = 0
		}
		if d := l - pl; d >= 8192 || d <= -8192 {
			bigSteps++
		}
	}
	return holes, bigSteps
}

// longestZeroRun — самая длинная серия нулевых пар отсчётов на выходе.
func (r continuityResult) longestZeroRun() int {
	best, run := 0, 0
	for i := 0; i+4 <= len(r.pcm); i += 4 {
		if binary.LittleEndian.Uint32(r.pcm[i:]) == 0 {
			run++
			best = max(best, run)
		} else {
			run = 0
		}
	}
	return best
}

func uniformDelay(loMS, hiMS float64) func(int, *rand.Rand) time.Duration {
	return func(_ int, r *rand.Rand) time.Duration {
		return time.Duration((loMS + r.Float64()*(hiMS-loMS)) * float64(time.Millisecond))
	}
}

// requireContinuous проверяет, что сборщик ничего не вставил и не обрезал и в
// выходе нет дыр и скачков, а длина выхода совпадает с входом после пересчёта частоты.
func requireContinuous(t *testing.T, r continuityResult, rate int) {
	t.Helper()
	st := r.stats
	if st.gapFills != 0 || st.trims != 0 || st.idleStalls != 0 || st.idleSamples != 0 {
		t.Errorf("сборщик вмешался в живой поток: вставок тишины %d (%.1f мс), обрезок %d (%.1f мс), простоев %d (%.1f мс), max |delta| PTS %v",
			st.gapFills, st.ms(st.gapFillSamples+0), st.trims, st.ms(st.trimSamples), st.idleStalls, st.ms(st.idleSamples), st.maxAbsDelta)
	}
	holes, big := r.defects()
	if holes != 0 || big != 0 {
		t.Errorf("в звуке дыр = %d, скачков = %d, ожидалось 0", holes, big)
	}
	want := r.inFrames * audioSampleRate / rate
	got := len(r.pcm) / 4
	if got < want-2*352 || got > want {
		t.Errorf("на выходе %d отсчётов, на входе после пересчёта %d (допуск: неполный последний кадр)", got, want)
	}
}

func TestContinuityLatePacketDelivery(t *testing.T) {
	for _, tc := range []struct {
		name string
		sim  continuitySim
	}{
		{"48k float запаздывание 0-15 мс", continuitySim{rate: 48000, float: true, delay: uniformDelay(0, 15)}},
		{"48k float запаздывание 8-12 мс", continuitySim{rate: 48000, float: true, delay: uniformDelay(8, 12)}},
		{"44,1k int16 запаздывание 0-15 мс", continuitySim{rate: 44100, delay: uniformDelay(0, 15)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sim := tc.sim
			sim.dur, sim.seed = 8*time.Second, 1
			requireContinuous(t, sim.run(), sim.rate)
		})
	}
}

func TestContinuityPTSJitter(t *testing.T) {
	for _, jit := range []time.Duration{time.Millisecond, 5 * time.Millisecond, 15 * time.Millisecond} {
		sim := continuitySim{rate: 48000, float: true, dur: 8 * time.Second, seed: 2, delay: uniformDelay(0, 1), ptsJit: jit}
		r := sim.run()
		requireContinuous(t, r, sim.rate)
		if r.stats.maxAbsDelta < jit/4 {
			t.Errorf("jit %v: max |delta| PTS %v — стенд не создал ожидаемой дрожи", jit, r.stats.maxAbsDelta)
		}
	}
}

// Грубый тик 15,6 мс: PTS идут из qpcClock, якорь обновляется каждую секунду.
func TestContinuityCoarseTimerTick(t *testing.T) {
	sim := continuitySim{rate: 48000, float: true, dur: 61 * time.Second, seed: 3,
		delay: uniformDelay(0, 15), tick: 15600 * time.Microsecond}
	r := sim.run()
	requireContinuous(t, r, sim.rate)
	if r.maxClockErr > time.Millisecond {
		t.Errorf("qpcClock ошибается до %v на грубом тике, ожидалось < 1 мс", r.maxClockErr)
	}
}

// Настоящая пауза: звук встал на 500 мс. Тишины должно оказаться столько же.
func TestContinuityRealPauseInsertsMatchingSilence(t *testing.T) {
	const pause = 500 * time.Millisecond
	sim := continuitySim{rate: 48000, float: true, dur: 3 * time.Second, seed: 4,
		delay: uniformDelay(0, 4), pauseFrom: time.Second, pauseTo: time.Second + pause}
	r := sim.run()
	want := int(pause * audioSampleRate / time.Second)
	got := r.longestZeroRun()
	// Допуск — один пакет (10 мс): тишина по простою кончается у конца уже
	// заполненной временной шкалы, а не ровно на метке первого пакета после паузы.
	if tol := int(contPacketTime * audioSampleRate / time.Second); got < want-tol || got > want+tol {
		t.Errorf("тишина паузы = %d отсчётов (%.1f мс), ожидалось %d ± %d", got, float64(got)*1000/audioSampleRate, want, tol)
	}
	if r.stats.idleStalls != 1 {
		t.Errorf("эпизодов простоя = %d, ожидался 1", r.stats.idleStalls)
	}
}

// То же на уровне сборщика, без опроса: разрыв PTS в 500 мс даёт ровно
// samplesAt(500 мс) отсчётов тишины.
func TestAssemblerRealPauseInsertsExactSilence(t *testing.T) {
	base := time.Unix(7000, 0)
	a := newPCMFrameAssembler(352)
	a.setInputRate(44100)
	a.writeStereo(stereoRamp(441, 0.5), base)
	resume := base.Add(10*time.Millisecond + 500*time.Millisecond)
	a.writeStereo(stereoRamp(441, 0.5), resume)
	a.writeStereo(stereoRamp(441, 0.5), resume.Add(10*time.Millisecond))
	var pcm []byte
	for _, f := range collectFrames(a) {
		pcm = append(pcm, f.pcm...)
	}
	zeros := 0
	for i := 0; i+4 <= len(pcm); i += 4 {
		if binary.LittleEndian.Uint32(pcm[i:]) == 0 {
			zeros++
		}
	}
	if want := 22050; zeros != want {
		t.Fatalf("нулевых отсчётов = %d, ожидалось ровно %d (500 мс при 44,1 кГц)", zeros, want)
	}
	if st := a.takeStats(); st.gapFills != 1 || st.gapFillSamples != 22050 {
		t.Fatalf("stats = %+v, ожидалась одна вставка 22050 отсчётов", st)
	}
}

// Мелкий скачок PTS (в пределах порога) не должен менять число отсчётов.
func TestAssemblerSmallPTSJumpsKeepSampleCount(t *testing.T) {
	base := time.Unix(8000, 0)
	a := newPCMFrameAssembler(352)
	a.setInputRate(44100)
	const packets = 100
	for i := 0; i < packets; i++ {
		pts := base.Add(audioSamplesDuration(uint64(i * 441)))
		if i%2 == 1 {
			pts = pts.Add(15 * time.Millisecond)
		} else if i > 0 {
			pts = pts.Add(-15 * time.Millisecond)
		}
		a.writeStereo(stereoRamp(441, 0.25), pts)
	}
	frames := collectFrames(a)
	if want := packets * 441 / 352; len(frames) != want && len(frames) != want-1 {
		t.Fatalf("кадров = %d, ожидалось %d: мелкие скачки PTS изменили число отсчётов", len(frames), want)
	}
	if st := a.takeStats(); st.gapFills != 0 || st.trims != 0 {
		t.Fatalf("stats = %+v, вставок и обрезок быть не должно", st)
	}
}

// driftRun подаёт сборщику 48 кГц пакеты по 10 мс, у которых PTS идут по
// системным часам, а часы устройства уходят от них на ppm (устройство быстрее —
// ppm > 0) и PTS дрожит на ±jit. Возвращает отклонения PTS выходных кадров от
// идеальных (без дрожи, по системным часам) и счётчики сборщика.
func driftRun(ppm float64, jit time.Duration, dur time.Duration) (maxErr time.Duration, st assemblerStats) {
	const rate, pkt = 48000, 480
	rng := rand.New(rand.NewSource(11))
	base := time.Unix(9000, 0)
	// системное время отсчёта устройства n: часы устройства идут быстрее на ppm
	sys := func(n uint64) time.Duration {
		return time.Duration(float64(n) / rate / (1 + ppm*1e-6) * float64(time.Second))
	}
	a := newPCMFrameAssembler(352)
	a.setInputRate(rate)
	samples := stereoRamp(pkt, 0.1)
	for n := uint64(0); n < uint64(dur/contPacketTime)*pkt; n += pkt {
		pts := base.Add(sys(n))
		if jit > 0 {
			pts = pts.Add(time.Duration((rng.Float64()*2 - 1) * float64(jit)))
		}
		a.writeStereo(samples, pts)
	}
	for i, f := range collectFrames(a) {
		if i*352 < audioSampleRate { // первую секунду пропускаем: подстройка сходится
			continue
		}
		ideal := base.Add(sys(uint64(i*352) * rate / audioSampleRate))
		e := f.position.PTS.Sub(ideal)
		maxErr = max(maxErr, max(e, -e))
	}
	return maxErr, a.takeStats()
}

// Часы устройства уходят от системных: подстройка PTS должна их догонять, не
// вставляя тишину и не режа отсчёты. Без неё за 5 минут при 500 ppm набегает
// 150 мс, то есть порог пересинхронизации (40 мс) срабатывает трижды.
func TestAssemblerSlewTracksDeviceClockDrift(t *testing.T) {
	for _, ppm := range []float64{100, 500, -300} {
		maxErr, st := driftRun(ppm, 0, 5*time.Minute)
		if st.gapFills != 0 || st.trims != 0 {
			t.Errorf("%+.0f ppm: вставок %d, обрезок %d, ожидалось 0", ppm, st.gapFills, st.trims)
		}
		if st.maxAbsDelta > time.Millisecond {
			t.Errorf("%+.0f ppm: max |delta| PTS %v, подстройка не догоняет дрейф (ожидалось < 1 мс)", ppm, st.maxAbsDelta)
		}
		if maxErr > time.Millisecond {
			t.Errorf("%+.0f ppm: PTS кадров отклоняется до %v от системных часов (ожидалось < 1 мс)", ppm, maxErr)
		}
	}
}

// Дрожь PTS ±10 мс не должна попадать в метки выходных кадров: подстройка
// сглаживает её (делитель 32). Если метка слепо следует за каждым пакетом,
// ошибка достигает амплитуды дрожи.
func TestAssemblerSlewSmoothsPTSJitter(t *testing.T) {
	maxErr, st := driftRun(200, 10*time.Millisecond, 2*time.Minute)
	if st.gapFills != 0 || st.trims != 0 {
		t.Errorf("вставок %d, обрезок %d, ожидалось 0", st.gapFills, st.trims)
	}
	if maxErr > 4*time.Millisecond {
		t.Errorf("PTS кадров отклоняется до %v при дрожи ±10 мс, подстройка не сглаживает (ожидалось < 4 мс)", maxErr)
	}
}
