//go:build windows

// Пакет native — источник видео AirSpoon без ffmpeg: захват экрана DXGI Desktop
// Duplication (internal/capture/dxgi), преобразование цвета на GPU и кодирование
// H.264 через Media Foundation (internal/encode/mf). Реализует airplay.VideoSource.
package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"airspoon/internal/airplay"
	"airspoon/internal/capture/dxgi"
	"airspoon/internal/encode/ffmpeg"
	"airspoon/internal/encode/mf"
	"airspoon/internal/win/com"
)

// Mode — выбор кодера.
type Mode int

const (
	// ModeAuto — сначала аппаратный кодер, при отказе программный.
	ModeAuto Mode = iota
	// ModeHardware — только аппаратный (с запасным входом через системную память).
	ModeHardware
	// ModeSoftware — только программный Microsoft H.264 Encoder MFT.
	ModeSoftware
)

func (m Mode) String() string {
	switch m {
	case ModeHardware:
		return "hw"
	case ModeSoftware:
		return "sw"
	}
	return "auto"
}

// ParseMode разбирает значение флага --encoder (кроме ffmpeg).
func ParseMode(s string) (Mode, error) {
	switch s {
	case "auto":
		return ModeAuto, nil
	case "hw":
		return ModeHardware, nil
	case "sw":
		return ModeSoftware, nil
	}
	return ModeAuto, fmt.Errorf("неизвестный кодер %q (нужно auto, hw, sw или ffmpeg)", s)
}

// Config — параметры источника.
type Config struct {
	Mode Mode
	// Display — номер монитора (0 — первый).
	Display int
	// Width, Height — размер видео; по умолчанию 1280x720.
	Width, Height int
	// FPS — частота кадров; по умолчанию 30.
	FPS int
	// BitrateKbps — битрейт; 0 — как у ffmpeg-источника.
	BitrateKbps int
	// ShowCursor — рисовать указатель мыши.
	ShowCursor bool
	// DisableTextures запрещает передавать кадры аппаратному кодеру текстурами
	// GPU (кадр читается в память): для диагностики и как ручной запасной путь.
	DisableTextures bool
	// ForceCPU отключает видеопроцессор GPU: цвет и масштаб преобразует CPU
	// (флаг --no-gpu-convert; запасной путь, если видеопроцессор драйвера даёт
	// артефакты или отказывает).
	ForceCPU bool
	// Logf — сообщения для пользователя; Debugf — подробности и статистика
	// раз в StatsInterval (может быть nil).
	Logf, Debugf func(format string, args ...any)
	// StatsInterval — период статистики в Debugf; по умолчанию 5 с.
	StatsInterval time.Duration
	// ChooseProfile вызывается, когда кодер уже выбран, но Start ещё не вернулся, и
	// ещё раз, если потом (зависание, сбой) замена кодера по цепочке запасных
	// сменила его класс; обратно на аппаратный источник не возвращается. По Info
	// (в нём Info.Class(): аппаратный или программный кодер) вызывающий решает,
	// годится ли заказанный размер и частота кадров. Если вернулось (профиль, true)
	// и профиль отличается от текущего, источник закрывает захват и кодер и
	// поднимает их заново с новыми параметрами (сразу, без повторного вызова
	// ChooseProfile для того же кодера). Битрейт 0 в профиле: если он был
	// вычислен автоматически — пересчитывается под новый размер, иначе остаётся.
	// Вызывается из рабочего потока источника: не блокировать надолго.
	ChooseProfile func(Info) (Profile, bool)

	// faultHook — подмена сбоя кодера для тестов без железа: вызывается после
	// каждого кадра, ненулевая ошибка трактуется как сбой работающего кодера.
	faultHook func(enc *mf.Encoder) error
}

// Info описывает, что именно запущено.
type Info struct {
	Encoder       string // имя MFT
	Hardware      bool
	Textures      bool   // кодер получает текстуры GPU, без копий в память
	GPUConvert    bool   // цвет и масштаб преобразует GPU
	Monitor       string // монитор и адаптер
	BitrateKbps   int
	Width, Height int
	FPS           int
}

// Stats — счётчики работы.
type Stats struct {
	Captured   uint64        // новых кадров рабочего стола
	Produced   uint64        // кадров, выданных захватом (включая повторы)
	Offered    uint64        // кадров, предложенных кодеру (принятых и отброшенных)
	Encoded    uint64        // пакетов, выданных кодером
	Skipped    uint64        // кадров, отброшенных до кодера (кодер занят)
	Dropped    uint64        // пакетов, отброшенных после кодера (потребитель отстаёт)
	AvgLatency time.Duration // захват -> выход кодера, среднее за всё время
	MaxLatency time.Duration
}

const (
	queueSize = 4
	// maxStallRestarts — сколько раз подряд пересоздавать последний кодер цепочки,
	// если он завис; счётчик обнуляется, когда кодер снова выдал заметное число
	// пакетов (stallRecoveredOutputs).
	maxStallRestarts      = 2
	stallRecoveredOutputs = 30
	// Проверка скорости входа-текстур: в каждом окне из textureProbeFrames кадров
	// (при 30 к/с) допустимо отбросить не больше textureMaxSkipPercent процентов.
	textureProbeFrames    = 30
	textureMaxSkipPercent = 25
)

// Source — нативный источник видео.
type Source struct {
	cfg  Config
	base time.Time // начало отсчёта меток времени образцов

	out  chan airplay.VideoAccessUnit
	stop chan struct{}
	done chan struct{}

	errMu sync.Mutex
	err   error

	closeOnce sync.Once
	needKey   atomic.Bool
	enc       atomic.Pointer[mf.Encoder]
	info      atomic.Pointer[Info]
	capt      atomic.Pointer[dxgi.Capturer]

	autoBitrate bool // битрейт вычислен автоматически (см. Config.ChooseProfile)
	// softOnly: в цепочке только программный кодер. Ставится при перезапуске под
	// меньший профиль после ухода с аппаратного кодера, чтобы пересозданный конвейер
	// не вернулся к аппаратному кодеру, который только что отказал.
	softOnly bool

	offered, skipped, dropped, encoded atomic.Uint64
	latencySum, latencyCount           atomic.Int64
	latencyMax, latencyWin             atomic.Int64 // за всё время и за текущий период статистики
}

var _ airplay.VideoSource = (*Source)(nil)

// Start запускает захват и кодирование. Ошибка возвращается, если не удалось
// открыть монитор или ни один кодер не запустился; временная недоступность
// рабочего стола (блокировка, UAC) ошибкой не считается. Источник
// останавливается при отмене ctx и при Close.
func Start(ctx context.Context, cfg Config) (*Source, error) {
	if cfg.Width == 0 {
		cfg.Width = 1280
	}
	if cfg.Height == 0 {
		cfg.Height = 720
	}
	if cfg.FPS == 0 {
		cfg.FPS = 30
	}
	cfg.Width &^= 1
	cfg.Height &^= 1
	autoBitrate := cfg.BitrateKbps == 0
	if cfg.BitrateKbps == 0 {
		cfg.BitrateKbps = ffmpeg.AutoBitrateKbps(cfg.Width, cfg.Height, cfg.FPS)
	}
	if cfg.StatsInterval <= 0 {
		cfg.StatsInterval = 5 * time.Second
	}
	s := &Source{
		cfg:         cfg,
		autoBitrate: autoBitrate,
		base:        time.Now(),
		out:         make(chan airplay.VideoAccessUnit, queueSize),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	ready := make(chan error, 1)
	go s.run(ready)
	select {
	case err := <-ready:
		if err != nil {
			<-s.done
			return nil, err
		}
	case <-ctx.Done():
		s.Close()
		return nil, ctx.Err()
	}
	go func() {
		select {
		case <-ctx.Done():
			s.Close()
		case <-s.done:
		}
	}()
	return s, nil
}

// Info возвращает описание запущенного кодера и захвата.
func (s *Source) Info() Info {
	if i := s.info.Load(); i != nil {
		return *i
	}
	return Info{}
}

// Stats возвращает счётчики; безопасно из любой горутины.
func (s *Source) Stats() Stats {
	st := Stats{Offered: s.offered.Load(), Skipped: s.skipped.Load(), Dropped: s.dropped.Load(), Encoded: s.encoded.Load(), MaxLatency: time.Duration(s.latencyMax.Load())}
	if c := s.capt.Load(); c != nil {
		cs := c.Stats()
		st.Captured, st.Produced = cs.Captured, cs.Produced
	}
	if n := s.latencyCount.Load(); n > 0 {
		st.AvgLatency = time.Duration(s.latencySum.Load() / n)
	}
	return st
}

// ReadAccessUnit блокируется до следующего кадра. Если рабочий поток источника
// завершился с ошибкой, возвращает её; после Close — io.EOF.
func (s *Source) ReadAccessUnit(ctx context.Context) (airplay.VideoAccessUnit, error) {
	select {
	case au := <-s.out:
		return au, nil
	default:
	}
	select {
	case au := <-s.out:
		return au, nil
	case <-ctx.Done():
		return airplay.VideoAccessUnit{}, ctx.Err()
	case <-s.done:
		select {
		case au := <-s.out:
			return au, nil
		default:
		}
		if err := s.failure(); err != nil {
			return airplay.VideoAccessUnit{}, err
		}
		return airplay.VideoAccessUnit{}, io.EOF
	}
}

// Close останавливает поток захвата и кодирования и освобождает ресурсы.
func (s *Source) Close() error {
	s.closeOnce.Do(func() { close(s.stop) })
	<-s.done
	return nil
}

func (s *Source) failure() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}

func (s *Source) setFailure(err error) {
	s.errMu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.errMu.Unlock()
}

func (s *Source) logf(format string, args ...any) {
	if s.cfg.Logf != nil {
		s.cfg.Logf(format, args...)
	}
}

func (s *Source) debugf(format string, args ...any) {
	if s.cfg.Debugf != nil {
		s.cfg.Debugf(format, args...)
	}
}

// encSpec — вариант кодера в цепочке запасных.
type encSpec struct {
	hardware bool
	textures bool
}

func (e encSpec) String() string {
	switch {
	case !e.hardware:
		return "программный"
	case e.textures:
		return "аппаратный (текстуры GPU)"
	}
	return "аппаратный (системная память)"
}

func (s *Source) chain(hasVP bool) []encSpec {
	var c []encSpec
	if s.cfg.Mode != ModeSoftware && !s.softOnly {
		if hasVP && !s.cfg.DisableTextures {
			c = append(c, encSpec{hardware: true, textures: true})
		}
		c = append(c, encSpec{hardware: true})
	}
	if s.cfg.Mode != ModeHardware {
		c = append(c, encSpec{})
	}
	return c
}

func (s *Source) openEncoder(c *dxgi.Capturer, spec encSpec) (*mf.Encoder, error) {
	mcfg := mf.Config{
		Width: s.cfg.Width, Height: s.cfg.Height, FPS: s.cfg.FPS, BitrateKbps: s.cfg.BitrateKbps,
		Output: s.onPacket, Logf: s.cfg.Logf, Debugf: s.cfg.Debugf,
	}
	var enc *mf.Encoder
	var err error
	if spec.hardware {
		// Кодер той же видеокарты, что и захват (MFTEnum2 с LUID); иначе — любой
		// аппаратный через системную память (см. mf.NewHardware).
		if l := c.AdapterLUID(); !l.IsZero() {
			mcfg.AdapterLUID = &mf.AdapterLUID{Low: l.Low, High: l.High}
		}
		if spec.textures {
			mcfg.Device = c.Device()
		}
		enc, err = mf.NewHardware(mcfg)
	} else {
		enc, err = mf.NewSoftware(mcfg)
	}
	if err != nil {
		return nil, err
	}
	if spec.hardware && spec.textures && !enc.Textures() {
		// Этот MFT не принимает текстуры — вариант «через память» ему и соответствует.
		s.debugf("%s не принимает текстуры D3D11: вход через системную память", enc.Name())
	}
	if err := c.SetWantTexture(enc.Textures()); err != nil {
		enc.Close()
		return nil, err
	}
	return enc, nil
}

// onPacket принимает кадр от кодера (из его потока) и кладёт в очередь чтения.
// Очередь короткая: если потребитель не успевает, накопленное выбрасывается
// целиком и ожидается новый IDR, потому что отдельные P-кадры выбросить нельзя.
func (s *Source) onPacket(p mf.Packet) {
	pts := s.base.Add(time.Duration(p.SampleTime) * 100)
	if lat := time.Since(pts); lat >= 0 {
		s.latencySum.Add(int64(lat))
		s.latencyCount.Add(1)
		raiseMax(&s.latencyMax, int64(lat))
		raiseMax(&s.latencyWin, int64(lat))
	}
	s.encoded.Add(1)
	if s.needKey.Load() {
		if !p.Key {
			s.dropped.Add(1)
			return
		}
		s.needKey.Store(false)
	}
	au := airplay.VideoAccessUnit{AnnexB: p.AnnexB, PTS: pts}
	select {
	case s.out <- au:
		return
	default:
	}
	// Очередь полна: выбрасываем накопленное и просим новый ключевой кадр.
	for {
		select {
		case <-s.out:
			s.dropped.Add(1)
			continue
		default:
		}
		break
	}
	s.dropped.Add(1)
	s.needKey.Store(true)
	if enc := s.enc.Load(); enc != nil {
		enc.ForceKeyFrame()
	}
}

// pipeline — захват экрана и работающий кодер.
type pipeline struct {
	capt     *dxgi.Capturer
	enc      *mf.Encoder
	chain    []encSpec
	idx      int // номер работающего варианта в chain
	failures []string
}

// openPipeline открывает захват и первый запустившийся кодер цепочки.
func (s *Source) openPipeline() (*pipeline, error) {
	capt, err := dxgi.New(dxgi.Config{
		Display: s.cfg.Display, Width: s.cfg.Width, Height: s.cfg.Height, FPS: s.cfg.FPS,
		ShowCursor: s.cfg.ShowCursor, WantTexture: false, ForceCPU: s.cfg.ForceCPU,
		Logf: s.cfg.Logf, Debugf: s.cfg.Debugf,
	})
	if err != nil {
		return nil, fmt.Errorf("захват экрана: %w", err)
	}
	s.capt.Store(capt)
	p := &pipeline{capt: capt, chain: s.chain(capt.HasVideoProcessor())}
	var noMF error
	for ; p.idx < len(p.chain); p.idx++ {
		if p.enc, err = s.openEncoder(capt, p.chain[p.idx]); err == nil {
			break
		}
		p.failures = append(p.failures, fmt.Sprintf("%s: %v", p.chain[p.idx], err))
		s.debugf("кодер (%s) не запущен: %v", p.chain[p.idx], err)
		if errors.Is(err, mf.ErrUnavailable) {
			noMF = err
		}
	}
	if p.enc == nil {
		s.capt.Store(nil)
		capt.Close()
		if noMF != nil {
			// Нет Media Foundation или программного кодера (Windows N без пакета
			// мультимедийных функций): признак остаётся в цепочке для трея.
			return nil, fmt.Errorf("видеокодер H.264 не запущен (%v): %w", p.failures, noMF)
		}
		return nil, fmt.Errorf("видеокодер H.264 не запущен (%v)", p.failures)
	}
	s.enc.Store(p.enc)
	return p, nil
}

// closePipeline останавливает кодер, затем захват.
func (s *Source) closePipeline(p *pipeline) {
	s.enc.Store(nil)
	if p.enc != nil {
		p.enc.Close()
		p.enc = nil
	}
	p.capt.Close()
}

// run — рабочий поток: захват и кодирование на одном закреплённом потоке ОС.
func (s *Source) run(ready chan<- error) {
	defer close(s.done)
	cleanup, err := com.Init()
	if err != nil {
		ready <- err
		return
	}
	defer cleanup()

	p, err := s.openPipeline()
	if err != nil {
		ready <- err
		return
	}
	// Класс кодера известен только теперь: вызывающий может попросить меньший
	// профиль (программный кодер не тянет 1080p60), тогда всё поднимается заново.
	if p, err = s.rechoose(p, false, ClassSoftware); err != nil {
		ready <- err
		return
	}
	// p == nil после неудачного пересоздания в цикле: openPipeline уже всё закрыл.
	defer func() {
		if p != nil {
			s.closePipeline(p)
		}
	}()
	s.publishInfo(p.capt, p.enc)
	if len(p.failures) > 0 {
		s.logf("предыдущие варианты кодера не подошли (%d); работает: %s", len(p.failures), p.enc.Name())
	}
	ready <- nil

	statsDone := make(chan struct{})
	defer close(statsDone)
	go s.statsLoop(statsDone)

	var lastST int64
	var grabs, skipped int // кадры текущего окна проверки скорости входа-текстур
	stallRestarts := 0
	for {
		enc := p.enc
		f, err := p.capt.Grab(s.stop)
		if err != nil && errors.Is(err, dxgi.ErrVideoProcessor) && p.chain[p.idx].textures {
			// Видеопроцессор GPU отказал при кадрах-текстурах: переходим на
			// следующий вариант цепочки (вход через системную память; захват
			// сам отключит видеопроцессор).
			s.logf("видеопроцессор GPU отказал (%v); переключаемся на вход через системную память", err)
			prev := classOf(enc.Hardware())
			enc.Close()
			s.enc.Store(nil)
			p.enc = nil
			if rerr := s.recoverEncoder(p, err, &stallRestarts); rerr != nil {
				s.setFailure(rerr)
				return
			}
			s.enc.Store(p.enc)
			s.needKey.Store(false)
			grabs, skipped = 0, 0
			if p, err = s.rechoose(p, true, prev); err != nil {
				p = nil
				s.setFailure(err)
				return
			}
			s.publishInfo(p.capt, p.enc)
			continue
		}
		if err != nil {
			if !errors.Is(err, dxgi.ErrStopped) {
				s.setFailure(fmt.Errorf("захват экрана: %w", err))
			}
			return
		}
		// Видеопроцессор GPU мог отключиться внутри Grab (dropVideoProc): цвет теперь
		// преобразует CPU, а Info и журнал иначе остались бы с «на GPU».
		s.syncConvertPath(p.capt)
		st := int64(f.At.Sub(s.base) / 100)
		if st <= lastST {
			st = lastST + 1
		}
		lastST = st
		var accepted bool
		if f.Texture != nil {
			accepted, err = enc.EncodeTexture(f.Texture, st)
		} else {
			accepted, err = enc.EncodeNV12(f.Data, st)
		}
		grabs++
		s.offered.Add(1)
		if err == nil && !accepted {
			s.skipped.Add(1)
			skipped++
		}
		if err == nil {
			err = enc.Err()
		}
		// Зависание: кадры подаются, а пакетов нет, либо запрошенный ключевой кадр
		// не приходит (mf.Encoder.Health).
		if err == nil {
			err = enc.Health()
		}
		if err == nil && s.cfg.faultHook != nil {
			err = s.cfg.faultHook(enc)
		}
		if err == nil && stallRestarts > 0 && enc.Outputs() >= stallRecoveredOutputs {
			stallRestarts = 0
		}
		// Вход текстурами у части драйверов работает заметно медленнее входа через
		// память, особенно пока рабочий стол меняется (измерено на NVIDIA MFT:
		// ~14 к/с и задержка 50 мс против 30 к/с и 10 мс). Окно проверки скользит
		// всё время работы: если кодер не принимает больше textureMaxSkipPercent
		// процентов кадров, переходим на вход через память.
		slowTextures := false
		if err == nil && p.chain[p.idx].textures && grabs >= textureProbeFrames*s.cfg.FPS/30 {
			slowTextures = skipped*100/grabs > textureMaxSkipPercent
			if !slowTextures {
				grabs, skipped = 0, 0
			}
		}
		if slowTextures {
			err = fmt.Errorf("вход текстурами слишком медленный: отброшено %d кадров из %d", skipped, grabs)
			s.logf("%s: %v; переключаемся на вход через системную память", enc.Name(), err)
		} else if err != nil {
			// Сбой кодера: пересоздаём его или переходим на следующий вариант цепочки.
			s.logf("сбой кодера %s: %v", enc.Name(), err)
		}
		if err == nil {
			continue
		}
		prev := classOf(enc.Hardware())
		enc.Close()
		s.enc.Store(nil)
		p.enc = nil
		if err := s.recoverEncoder(p, err, &stallRestarts); err != nil {
			s.setFailure(err)
			return
		}
		s.enc.Store(p.enc)
		s.needKey.Store(false)
		grabs, skipped = 0, 0
		if p, err = s.rechoose(p, true, prev); err != nil {
			p = nil
			s.setFailure(err)
			return
		}
		s.publishInfo(p.capt, p.enc)
	}
}

func classOf(hardware bool) EncoderClass { return Info{Hardware: hardware}.Class() }

// convertPather — то, что нужно syncConvertPath от захвата (*dxgi.Capturer).
type convertPather interface {
	ConvertPath() (gpu bool, reason string)
}

// syncConvertPath обновляет Info, если путь преобразования цвета у захвата
// изменился с тех пор, как Info опубликован (видеопроцессор GPU отключился посреди
// показа), и пишет причину в журнал. Вызывается из рабочего потока на каждом кадре.
func (s *Source) syncConvertPath(c convertPather) {
	gpu, reason := c.ConvertPath()
	cur := s.Info()
	if cur.GPUConvert == gpu {
		return
	}
	cur.GPUConvert = gpu
	s.info.Store(&cur)
	s.logf("путь преобразования цвета изменился: %s", convertPathText(gpu, reason))
	s.logf("видеокодер: %s", s.describe())
}

func convertPathText(gpu bool, reason string) string {
	if gpu {
		return "преобразование цвета на GPU"
	}
	if reason == "" {
		return "преобразование цвета на CPU"
	}
	return "преобразование цвета на CPU (" + reason + ")"
}

// rechoose спрашивает у ChooseProfile профиль для работающего кодера и, если он
// отличается от текущего, поднимает захват и кодер заново. onlyIfChanged: после
// замены кодера по цепочке запасных спрашиваем, только если класс кодера (prev)
// сменился; при старте спрашиваем всегда. После ухода с аппаратного кодера цепочка
// пересоздаётся без аппаратных вариантов (softOnly). При ошибке пересоздания
// возвращается (nil, err): openPipeline уже всё освободил.
func (s *Source) rechoose(p *pipeline, onlyIfChanged bool, prev EncoderClass) (*pipeline, error) {
	choose := s.cfg.ChooseProfile
	if choose == nil {
		return p, nil
	}
	info := s.makeInfo(p.capt, p.enc)
	if onlyIfChanged && info.Class() == prev {
		return p, nil
	}
	prof, ok := choose(info)
	if !ok {
		return p, nil
	}
	cfg, changed := applyProfile(s.cfg, s.autoBitrate, prof)
	if !changed {
		return p, nil
	}
	s.logf("кодер %q (%s): профиль меняется с %s на %s", info.Encoder, info.Class(), profileOf(s.cfg), profileOf(cfg))
	if onlyIfChanged && info.Class() == ClassSoftware {
		s.softOnly = true
	}
	s.capt.Store(nil)
	s.closePipeline(p)
	// Только профильные поля: statsLoop читает остальные поля s.cfg из другой горутины.
	s.cfg.Width, s.cfg.Height, s.cfg.FPS, s.cfg.BitrateKbps = cfg.Width, cfg.Height, cfg.FPS, cfg.BitrateKbps
	np, err := s.openPipeline()
	if err != nil {
		return nil, err
	}
	return np, nil
}

// recoverEncoder подбирает замену отказавшему кодеру (p.enc уже закрыт).
// Отказ по зависанию (mf.IsStall) сначала лечится пересозданием того же варианта,
// до maxStallRestarts раз подряд; затем, как и любой другой сбой, переходом на
// следующий вариант цепочки (там счётчик начинается заново). Когда варианты
// кончились, источник останавливается с ошибкой.
func (s *Source) recoverEncoder(p *pipeline, cause error, restarts *int) error {
	lastErr := cause
	if mf.IsStall(cause) && *restarts < maxStallRestarts {
		*restarts++
		s.logf("кодер завис, пересоздаём тот же вариант (%s), попытка %d из %d", p.chain[p.idx], *restarts, maxStallRestarts)
		enc, err := s.openEncoder(p.capt, p.chain[p.idx])
		if err == nil {
			p.enc = enc
			return nil
		}
		lastErr = err
		s.debugf("кодер (%s) не пересоздан: %v", p.chain[p.idx], err)
	}
	*restarts = 0
	for p.idx++; p.idx < len(p.chain); p.idx++ {
		enc, err := s.openEncoder(p.capt, p.chain[p.idx])
		if err == nil {
			p.enc = enc
			return nil
		}
		lastErr = err
		s.debugf("кодер (%s) не запущен: %v", p.chain[p.idx], err)
	}
	return fmt.Errorf("все варианты видеокодера H.264 отказали: %w", lastErr)
}

func (s *Source) makeInfo(c *dxgi.Capturer, enc *mf.Encoder) Info {
	return Info{
		Encoder: enc.Name(), Hardware: enc.Hardware(), Textures: enc.Textures(),
		GPUConvert: c.HasVideoProcessor(), Monitor: c.MonitorInfo(),
		BitrateKbps: s.cfg.BitrateKbps, Width: s.cfg.Width, Height: s.cfg.Height, FPS: s.cfg.FPS,
	}
}

func (s *Source) publishInfo(c *dxgi.Capturer, enc *mf.Encoder) {
	info := s.makeInfo(c, enc)
	s.info.Store(&info)
	s.logf("видеокодер: %s", s.describe())
}

func (s *Source) describe() string {
	i := s.Info()
	kind := "программный"
	if i.Hardware {
		kind = "аппаратный"
	}
	path := "кадры через системную память"
	if i.Textures {
		path = "кадры остаются на GPU"
	}
	conv := "преобразование цвета на CPU"
	if i.GPUConvert {
		conv = "преобразование цвета на GPU"
	}
	return fmt.Sprintf("%q, %s, %s, %s; %dx%d@%d, %d кбит/с; монитор %s",
		i.Encoder, kind, path, conv, i.Width, i.Height, i.FPS, i.BitrateKbps, i.Monitor)
}

// statsLoop раз в StatsInterval пишет в подробный журнал фактическую частоту
// захвата и кодирования, а раз в problemLogInterval — в обычный журнал строку о
// проблемах, но только если они были (statsProblem).
func (s *Source) statsLoop(done <-chan struct{}) {
	if s.cfg.Debugf == nil && s.cfg.Logf == nil {
		return
	}
	t := time.NewTicker(s.cfg.StatsInterval)
	defer t.Stop()
	prev := s.Stats()
	prevAt := time.Now()
	var prevLatSum, prevLatCount int64
	// Окно для строки о проблемах.
	probPrev, probAt := prev, prevAt
	var probLatSum, probLatCount int64
	for {
		select {
		case <-done:
			return
		case <-s.stop:
			return
		case now := <-t.C:
			cur := s.Stats()
			sum, cnt := s.latencySum.Load(), s.latencyCount.Load()
			if s.cfg.Debugf != nil {
				dt := now.Sub(prevAt).Seconds()
				var avg time.Duration
				if cnt > prevLatCount {
					avg = time.Duration((sum - prevLatSum) / (cnt - prevLatCount))
				}
				s.debugf("[ВИДЕО] за %.0f с: захват экрана %.1f к/с (новых), выдача кадров %.1f к/с, кодирование %.1f к/с; "+
					"задержка захват->кодер avg %v max %v; отброшено до кодера %d, после %d",
					dt, float64(cur.Captured-prev.Captured)/dt, float64(cur.Produced-prev.Produced)/dt,
					float64(cur.Encoded-prev.Encoded)/dt, avg.Round(100*time.Microsecond), time.Duration(s.latencyWin.Swap(0)).Round(100*time.Microsecond),
					cur.Skipped-prev.Skipped, cur.Dropped-prev.Dropped)
			}
			prev, prevAt, prevLatSum, prevLatCount = cur, now, sum, cnt

			// Строка о проблемах: окно не короче problemLogInterval (с допуском на тик).
			if now.Sub(probAt) < problemLogInterval-s.cfg.StatsInterval/2 {
				continue
			}
			w := statsWindow{
				Dur: now.Sub(probAt), Offered: cur.Offered - probPrev.Offered,
				Skipped: cur.Skipped - probPrev.Skipped, Dropped: cur.Dropped - probPrev.Dropped,
			}
			if cnt > probLatCount {
				w.AvgLatency = time.Duration((sum - probLatSum) / (cnt - probLatCount))
			}
			if msg := statsProblem(w); msg != "" {
				s.logf("%s", msg)
			}
			probPrev, probAt, probLatSum, probLatCount = cur, now, sum, cnt
		}
	}
}

// raiseMax повышает *v до x, если x больше.
func raiseMax(v *atomic.Int64, x int64) {
	for {
		m := v.Load()
		if x <= m || v.CompareAndSwap(m, x) {
			return
		}
	}
}
