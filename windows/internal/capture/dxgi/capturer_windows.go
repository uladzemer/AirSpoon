//go:build windows && (amd64 || arm64)

package dxgi

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"
	"unsafe"

	"airspoon/internal/win/com"
)

// Config — параметры захвата.
type Config struct {
	// Display — сквозной номер монитора (см. ListOutputs).
	Display int
	// Width, Height — размер холста NV12 на выходе (чётные); по умолчанию 1280x720.
	Width, Height int
	// FPS — частота выдачи кадров (по умолчанию 30). Если рабочий стол не
	// менялся, предыдущий кадр выдаётся повторно с этой же частотой.
	FPS int
	// ShowCursor — рисовать указатель мыши поверх кадра.
	ShowCursor bool
	// ForceCPU отключает видеопроцессор GPU: преобразование цвета и масштаба в Go
	// (опция «не использовать GPU-преобразование», флаг --no-gpu-convert). Кадры
	// текстурой (WantTexture) при этом недоступны. Захват переходит на CPU и сам,
	// если устройство создано без видео или видеопроцессор отказал.
	ForceCPU bool
	// WantTexture — выдавать кадры текстурой NV12 на GPU (для аппаратного
	// кодера); иначе кадр копируется в системную память.
	WantTexture bool
	// Logf — сообщения, видимые пользователю; Debugf — подробности (может быть nil).
	Logf, Debugf func(format string, args ...any)
}

// Frame — подготовленный кадр NV12 размера холста.
type Frame struct {
	// Texture — текстура NV12 на устройстве Capturer (кольцо; действует, пока
	// кольцо не обойдёт её целиком). Заполнена, если запрошена WantTexture.
	Texture *com.Object
	// Data — NV12 в памяти без выравнивания строк; действует до следующего Grab.
	Data []byte
	// At — момент, к которому относится содержимое кадра (захват рабочего стола
	// или выдача повтора).
	At time.Time
	// Fresh — содержимое отличается от предыдущего кадра.
	Fresh bool
}

// Stats — счётчики захвата (читаются из любой горутины).
type Stats struct {
	Captured uint64 // новых кадров рабочего стола
	Produced uint64 // выданных кадров (включая повторы)
	Lost     uint64 // потерь дублирования (ACCESS_LOST и другие)
}

const (
	ringSize         = 8
	dupRetryInterval = 250 * time.Millisecond
	dupLogInterval   = 5 * time.Second
	// Столько раз подряд (по 250 мс) DuplicateOutput должен вернуть
	// DXGI_ERROR_UNSUPPORTED на гибридной системе до первого кадра, чтобы
	// сообщить об ошибке вместо бесконечных повторов.
	unsupportedFailAfter = 3
	// То же на системе с одной видеокартой: там UNSUPPORTED изредка бывает
	// временным (смена режима, блокировка), поэтому ждём дольше (5 с). Потом: в
	// сеансе RDP или на Basic Render он постоянный, и пользователь получает
	// причину (UnsupportedError); иначе DesktopWaitError, и показ
	// переподключается с паузой, а не останавливается.
	unsupportedFailAfterSingle = 20
	// После первого кадра: столько подряд нет новых кадров из-за
	// UNSUPPORTED/NOT_FOUND (монитор исчез, Win+P, смена карты), и захват нужно
	// создать заново.
	staleAfter        = 10 * time.Second
	firstFrameWait    = 400 * time.Millisecond
	maxCursorSideSize = 512
)

// Capturer захватывает один монитор и выдаёт кадры NV12 с постоянной частотой.
// Все методы, кроме Stats и Close, вызываются из одного потока ОС, в котором
// создан Capturer (см. com.Init).
type Capturer struct {
	cfg      Config
	interval time.Duration
	adapter  string
	monitor  string
	rotation uint32 // DXGI_MODE_ROTATION последнего выбранного выхода
	luid     LUID   // видеокарта, на которой создано устройство D3D11

	// В системе больше одной настоящей видеокарты: тогда DXGI_ERROR_UNSUPPORTED
	// при самом первом открытии дублирования — признак «процесс на одной карте,
	// монитор на другой», и ждать бесполезно (см. ErrDuplicateUnsupported).
	hybrid      bool
	basic       bool        // монитор на программном адаптере (Basic Render)
	remoteFn    func() bool // подмена isRemoteSession для тестов
	unsupported int         // подряд неудачных открытий с DXGI_ERROR_UNSUPPORTED
	badSince    time.Time   // после первого кадра: с какого момента подряд UNSUPPORTED/NOT_FOUND

	// Как создано устройство и какой путь преобразования выбран (для diag).
	devInfo     deviceCreateResult
	gpuPath     bool   // преобразование цвета на GPU (видеопроцессор)
	pathReason  string // почему выбран текущий путь
	frameFormat uint32 // DXGI_FORMAT последнего полученного кадра рабочего стола

	// Подмены для тестов (nil — обычное поведение): открытие дублирования и
	// проверка потери устройства.
	openDupFn       func() error
	deviceRemovedFn func() bool

	device, ctx *com.Object
	dup         *com.Object
	dupRetryAt  time.Time
	dupLogAt    time.Time
	dupErr      error

	srcW, srcH   int    // native (unrotated) surface dimensions
	sizeRotation uint32 // rotation used by current resources/fit; may lag reopen until a frame arrives
	fit          Rect
	desktop      *com.Object // последний кадр рабочего стола (BGRA)
	composed     *com.Object // desktop + указатель
	deskView     *com.Object
	compView     *com.Object

	vp        *videoProc
	ring      []*com.Object
	ringViews []*com.Object
	ringPos   int

	stagingFull   *com.Object // BGRA для чтения на CPU
	stagingNV12   *com.Object // NV12 для чтения результата GPU
	stagingCursor *com.Object
	stagingCurW   int
	stagingCurH   int
	data          []byte
	wantTexture   bool
	warnedFormat  bool

	shape        *CursorShape
	surfaceShape *CursorShape // inverse-rotated shape, cached on shape/rotation changes
	curVisible   bool
	curX, curY   int

	haveDesktop     bool
	dirty           bool
	version         uint64 // растёт при любом изменении содержимого (рабочий стол, указатель)
	composedVersion uint64
	dataVersion     uint64
	contentAt       time.Time
	firstDeadline   time.Time
	next            time.Time
	closed          bool

	captured, produced, lost atomic.Uint64
}

// New открывает монитор, создаёт устройство D3D11, видеопроцессор и кольцо
// текстур NV12. Само дублирование рабочего стола открывается при первом Grab и
// при сбоях переоткрывается; недоступность рабочего стола (блокировка, UAC,
// сеанс без экрана) не считается ошибкой New.
func New(cfg Config) (*Capturer, error) {
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
	if cfg.Width < 2 || cfg.Height < 2 || cfg.FPS < 1 || cfg.FPS > 240 || cfg.Display < 0 {
		return nil, fmt.Errorf("недопустимые параметры захвата: %dx%d@%d, монитор %d", cfg.Width, cfg.Height, cfg.FPS, cfg.Display)
	}
	c := &Capturer{
		cfg:         cfg,
		interval:    time.Second / time.Duration(cfg.FPS),
		wantTexture: cfg.WantTexture,
	}
	found, err := findOutput(cfg.Display)
	if err != nil {
		if errors.Is(err, errNoDisplay) {
			return nil, c.noDisplayError(err)
		}
		return nil, err
	}
	defer found.release()
	c.adapter, c.monitor = found.info.AdapterName, found.info.Name
	c.noteRotation(found.info.Rotation)
	c.luid = found.info.AdapterLUID
	c.hybrid = hybridSystem()
	c.basic = adapterIsBasic(c.luid)
	c.device, c.ctx, c.devInfo, err = createDeviceInfo(found.adapter)
	if err != nil {
		return nil, err
	}
	// Холст NV12 и видеопроцессор.
	switch {
	case cfg.ForceCPU:
		c.setPath(false, "преобразование на CPU по настройке (не использовать GPU-преобразование)")
	case c.devInfo.NoVideo:
		// Устройство без видео (Basic Display, сломанный драйвер): видеопроцессор
		// не создаём, на нём бывают программные реализации с искажениями.
		c.setPath(false, fmt.Sprintf("устройство D3D11 создано без поддержки видео (HRESULT 0x%08X при VIDEO_SUPPORT)", c.devInfo.VideoHR))
		c.logf("видеопроцессор GPU не используется: %s; цвет преобразуется на CPU", c.pathReason)
	default:
		vp, verr := newVideoProc(c.device, c.ctx)
		if verr != nil {
			c.setPath(false, fmt.Sprintf("видеопроцессор GPU недоступен: %v", verr))
			c.logf("видеопроцессор GPU недоступен (%v); цвет преобразуется на CPU", verr)
		} else {
			c.vp = vp
			c.setPath(true, "видеопроцессор GPU принимает BGRA и выдаёт NV12")
		}
	}
	if err := c.createRing(); err != nil {
		if c.vp != nil && !cfg.WantTexture {
			c.logf("кольцо NV12 не создано (%v): преобразование на CPU", err)
			c.dropVideoProc(fmt.Sprintf("кольцо текстур NV12 не создано: %v", err))
		} else if cfg.WantTexture {
			c.Close()
			return nil, fmt.Errorf("текстуры NV12 недоступны: %w", err)
		}
	}
	b := found.info.Bounds
	// Output bounds are already upright; desktop textures must use native size.
	w, h := rotatedSize(b.W, b.H, c.rotation)
	if err := c.ensureSize(w, h); err != nil {
		c.Close()
		return nil, err
	}
	c.firstDeadline = time.Now().Add(firstFrameWait)
	return c, nil
}

func (c *Capturer) noDisplayError(base error) error {
	list, lerr := ListOutputs()
	if lerr != nil || len(list) == 0 {
		return fmt.Errorf("%w (доступных для захвата мониторов нет: рабочий стол недоступен в этом сеансе?)", base)
	}
	return fmt.Errorf("%w (доступно мониторов: %d)", base, len(list))
}

// AdapterLUID возвращает идентификатор видеокарты захвата: кодер выбирается
// на той же видеокарте (MFTEnum2 с MFT_ENUM_ADAPTER_LUID).
func (c *Capturer) AdapterLUID() LUID { return c.luid }

// Device возвращает устройство D3D11 (ID3D11Device) для аппаратного кодера.
func (c *Capturer) Device() *com.Object { return c.device }

// MonitorInfo — читаемое описание выбранного монитора.
func (c *Capturer) MonitorInfo() string {
	return fmt.Sprintf("%s на %s", c.monitor, c.adapter)
}

// HasVideoProcessor сообщает, работает ли преобразование цвета на GPU.
func (c *Capturer) HasVideoProcessor() bool { return c.vp != nil }

// SetWantTexture переключает выдачу кадров между текстурой GPU и памятью.
// Текстуру можно запросить только при наличии видеопроцессора.
func (c *Capturer) SetWantTexture(v bool) error {
	if v && c.vp == nil {
		return errors.New("видеопроцессор GPU недоступен: текстуры NV12 выдать нельзя")
	}
	if c.wantTexture && !v {
		// Texture frames advance dataVersion without updating c.data. The slow
		// texture-input fallback must read back the current upright frame even
		// when the desktop (and its version) has stopped changing.
		c.data = nil
	}
	c.wantTexture = v
	return nil
}

// Stats возвращает счётчики; безопасно из любой горутины.
func (c *Capturer) Stats() Stats {
	return Stats{Captured: c.captured.Load(), Produced: c.produced.Load(), Lost: c.lost.Load()}
}

func (c *Capturer) logf(format string, args ...any) {
	if c.cfg.Logf != nil {
		c.cfg.Logf(format, args...)
	}
}

func (c *Capturer) debugf(format string, args ...any) {
	if c.cfg.Debugf != nil {
		c.cfg.Debugf(format, args...)
	}
}

// createRing создаёт кольцо текстур NV12 размера холста (вход кодера).
func (c *Capturer) createRing() error {
	if c.vp == nil {
		return nil
	}
	for i := 0; i < ringSize; i++ {
		t, err := createTexture2D(c.device, &texture2DDesc{
			Width: uint32(c.cfg.Width), Height: uint32(c.cfg.Height), MipLevels: 1, ArraySize: 1,
			Format: dxgiFormatNV12, SampleCount: 1,
			Usage: d3d11UsageDefault, BindFlags: d3d11BindRenderTarget | d3d11BindShaderResource,
		})
		if err != nil {
			c.releaseRing()
			return err
		}
		c.ring = append(c.ring, t)
	}
	return nil
}

func (c *Capturer) releaseRing() {
	for _, v := range c.ringViews {
		v.Release()
	}
	c.ringViews = nil
	for _, t := range c.ring {
		t.Release()
	}
	c.ring = nil
}

// ensureSize (пере)создаёт ресурсы, зависящие от размера рабочего стола.
func (c *Capturer) ensureSize(w, h int) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("недопустимый размер рабочего стола %dx%d", w, h)
	}
	if c.desktop != nil && w == c.srcW && h == c.srcH && c.sizeRotation == c.rotation {
		return nil
	}
	c.releaseSize()
	c.srcW, c.srcH = w, h
	c.sizeRotation = c.rotation
	logicalW, logicalH := rotatedSize(w, h, c.rotation)
	// The receiver negotiates a fixed NV12 canvas, also used by the encoder.
	// Swap the desktop size, not that canvas: letterboxing preserves aspect
	// through runtime orientation changes without renegotiating the stream.
	c.fit = FitRect(logicalW, logicalH, c.cfg.Width, c.cfg.Height)
	c.surfaceShape = rotateCursorShape(c.shape, inverseRotation(c.rotation))
	// Некоторые драйверы (проверено на NVIDIA) не принимают в качестве входа
	// видеопроцессора текстуру только с флагом SHADER_RESOURCE, поэтому
	// пробуем варианты привязки по очереди.
	var vpErr error
	for i, bind := range []uint32{0, d3d11BindRenderTarget} {
		if err := c.createDesktopTextures(bind); err != nil {
			return err
		}
		if c.vp == nil {
			break
		}
		if vpErr = c.configureVP(); vpErr == nil {
			break
		}
		if i == 0 {
			c.releaseTexturesOnly()
		}
	}
	if c.vp != nil && vpErr != nil {
		if c.wantTexture {
			return c.failVideoProcSetup(vpErr)
		}
		c.logf("видеопроцессор GPU отключён (%v): преобразование цвета на CPU", vpErr)
		c.dropVideoProc(fmt.Sprintf("настройка видеопроцессора не удалась: %v", vpErr))
	}
	c.dirty = true
	c.version++
	c.debugf("размер рабочего стола %dx%d -> %dx%d (вписано %+v), GPU-преобразование: %t", w, h, c.cfg.Width, c.cfg.Height, c.fit, c.vp != nil)
	return nil
}

// failVideoProcSetup обрабатывает отказ видеопроцессора при настройке под новый
// размер, когда нужны кадры текстурой. Видеопроцессор снимается, а размер
// сбрасывается: иначе следующий ensureSize с тем же размером вышел бы раньше
// времени, и produce, видя vp != nil при пустом кольце, упал бы по индексу.
// Теперь следующий вызов пересоздаёт ресурсы на CPU-пути (после
// SetWantTexture(false) со стороны источника).
func (c *Capturer) failVideoProcSetup(vpErr error) error {
	c.dropVideoProc(fmt.Sprintf("настройка видеопроцессора не удалась: %v", vpErr))
	c.srcW, c.srcH = 0, 0
	return &VideoProcError{Err: fmt.Errorf("настройка видеопроцессора: %w", vpErr)}
}

func (c *Capturer) configureVP() error {
	if err := c.vp.configure(c.srcW, c.srcH, c.cfg.Width, c.cfg.Height, c.cfg.FPS, c.fit, c.rotation); err != nil {
		return err
	}
	var err error
	if c.deskView, err = c.vp.inputView(c.desktop); err != nil {
		return err
	}
	if c.compView, err = c.vp.inputView(c.composed); err != nil {
		return err
	}
	for _, v := range c.ringViews {
		v.Release()
	}
	c.ringViews = c.ringViews[:0]
	for _, t := range c.ring {
		v, err := c.vp.outputView(t)
		if err != nil {
			return err
		}
		c.ringViews = append(c.ringViews, v)
	}
	return nil
}

func (c *Capturer) releaseSize() {
	for _, o := range []**com.Object{&c.deskView, &c.compView, &c.desktop, &c.composed, &c.stagingFull} {
		(*o).Release()
		*o = nil
	}
	if c.vp != nil {
		c.vp.releaseProc()
	}
	for _, v := range c.ringViews {
		v.Release()
	}
	c.ringViews = nil
}

// Close освобождает все ресурсы. Повторный вызов безопасен.
func (c *Capturer) Close() {
	if c.closed {
		return
	}
	c.closed = true
	c.dup.Release()
	c.dup = nil
	c.releaseSize()
	c.releaseRing()
	if c.vp != nil {
		c.vp.release()
		c.vp = nil
	}
	c.stagingNV12.Release()
	c.stagingCursor.Release()
	c.stagingNV12, c.stagingCursor = nil, nil
	if c.ctx != nil {
		c.ctx.Call(slotCtxFlush)
	}
	c.ctx.Release()
	c.device.Release()
	c.ctx, c.device = nil, nil
}

// --- дублирование рабочего стола ---

// openDup пытается открыть дублирование выхода; ошибка означает «пока недоступно».
func (c *Capturer) openDup() error {
	// Ищем по запомненным имени монитора и LUID, а не по номеру: номера
	// сдвигаются при отключении монитора и смене основного.
	f, err := findOutputByIdentity(c.monitor, c.luid)
	if err != nil {
		return err
	}
	defer f.release()
	out1, err := f.output.QueryInterface(&iidOutput1)
	if err != nil {
		return fmt.Errorf("IDXGIOutput1: %w", err)
	}
	defer out1.Release()
	var dup *com.Object
	hr := out1.Call(slotOutput1DuplicateOutput, uintptr(unsafe.Pointer(c.device)), uintptr(unsafe.Pointer(&dup)))
	if err := com.Check("DuplicateOutput", hr); err != nil {
		return err
	}
	c.dup = dup
	// Read the rotation belonging to this duplication, avoiding a mode-change
	// race with the earlier output enumeration. Reopen after ACCESS_LOST does
	// this again, even when the native surface dimensions stay unchanged.
	var desc outduplDesc
	dup.Call(slotDupGetDesc, uintptr(unsafe.Pointer(&desc)))
	c.noteRotation(desc.Rotation)
	return nil
}

func (c *Capturer) noteRotation(rotation uint32) {
	if rotation == c.rotation {
		return
	}
	c.rotation = rotation
	c.curVisible = false // position from the previous orientation is stale
	// Keep the logical shape: DXGI need not resend an unchanged pointer image.
	// ensureSize rebuilds its inverse-rotated copy for the new surface.
	c.surfaceShape = nil
}

// tryOpenDup вызывает openDup не чаще раза в dupRetryInterval и не засоряет лог.
func (c *Capturer) tryOpenDup(now time.Time) error {
	if now.Before(c.dupRetryAt) {
		return nil
	}
	c.dupRetryAt = now.Add(dupRetryInterval)
	open := c.openDup
	if c.openDupFn != nil {
		open = c.openDupFn
	}
	err := open()
	if err == nil {
		c.unsupported = 0 // счёт «подряд»: успешное открытие его обрывает
		c.badSince = time.Time{}
		if c.dupErr != nil {
			c.logf("захват экрана восстановлен")
		}
		c.dupErr = nil
		c.dupLogAt = time.Time{}
		return nil
	}
	if c.deviceRemoved() {
		return ErrDeviceLost
	}
	c.dupErr = err
	if uerr := c.noteOpenError(err, now); uerr != nil {
		return uerr
	}
	if now.After(c.dupLogAt) {
		c.dupLogAt = now.Add(dupLogInterval)
		c.logf("рабочий стол недоступен для захвата (%v); повторяем попытку, пока выдаётся последний кадр", describeDupError(err))
	}
	return nil
}

// noteOpenError разбирает ошибку открытия дублирования.
//
// До первого кадра: подряд идущие DXGI_ERROR_UNSUPPORTED считаются, и после
// unsupportedFailAfter (гибридная система) или unsupportedFailAfterSingle (одна
// видеокарта) возвращается UnsupportedError с понятной причиной. На одной
// видеокарте порог выше: та же ошибка бывает временной (смена режима, блокировка
// экрана), а в сеансе RDP и виртуалке она постоянная.
//
// После первого кадра: если подряд (без успешного открытия и без новых кадров)
// идут UNSUPPORTED или NOT_FOUND (монитор исчез, Win+P, смена карты) дольше
// staleAfter, возвращается StaleError: Capturer нужно создать заново.
func (c *Capturer) noteOpenError(err error, now time.Time) error {
	hr, ok := com.HR(err)
	unsupported := ok && hr == hrUnsupported
	if c.haveDesktop {
		c.unsupported = 0
		gone := unsupported || (ok && hr == hrNotFound) || errors.Is(err, errNoDisplay)
		if !gone {
			c.badSince = time.Time{}
			return nil
		}
		if c.badSince.IsZero() {
			c.badSince = now
		}
		if d := now.Sub(c.badSince); d >= staleAfter {
			return &StaleError{Adapter: c.adapter, Monitor: c.monitor, Since: d, Err: err}
		}
		return nil
	}
	if !unsupported {
		c.unsupported = 0
		return nil
	}
	limit := unsupportedFailAfterSingle
	if c.hybrid {
		limit = unsupportedFailAfter
	}
	if c.unsupported++; c.unsupported >= limit {
		if c.hybrid || c.sessionWithoutDesktop() {
			return &UnsupportedError{Adapter: c.adapter, Monitor: c.monitor, Hybrid: c.hybrid, Err: err}
		}
		// Одна видеокарта, обычный сеанс: скорее всего экран блокировки или
		// смена режима. Не приговор: показ переподключится с паузой.
		return &DesktopWaitError{Adapter: c.adapter, Monitor: c.monitor, Err: err}
	}
	return nil
}

// sessionWithoutDesktop — захват здесь невозможен по природе сеанса: удалённый
// рабочий стол или монитор на Basic Render (виртуальная машина без драйвера).
func (c *Capturer) sessionWithoutDesktop() bool {
	if c.basic {
		return true
	}
	if c.remoteFn != nil {
		return c.remoteFn()
	}
	return isRemoteSession()
}

// adapterIsBasic сообщает, что адаптер с этим LUID программный (Microsoft Basic
// Render/Display). При ошибке перечисления считает, что нет.
func adapterIsBasic(luid LUID) bool {
	list, err := ListAdapters()
	if err != nil {
		return false
	}
	for _, a := range list {
		if a.LUID == luid {
			return IsBasicDisplay(a.VendorID, a.Flags)
		}
	}
	return false
}

// holdFrames сообщает, что кадры выдавать нельзя: до первого настоящего кадра
// дублирование не открывается из-за DXGI_ERROR_UNSUPPORTED. Чёрные кадры в этом
// состоянии дошли бы до приёмника раньше, чем набежит порог отказов, и показ
// начался бы с чёрного экрана, а понятная ошибка (и перезапуск на другой
// видеокарте) запоздала бы на минуты. Другие ошибки (блокировка экрана, UAC)
// кадры не задерживают: чёрный кадр там лучше тишины, а рабочий стол вернётся сам.
func (c *Capturer) holdFrames() bool {
	return c.dup == nil && !c.haveDesktop && c.unsupported > 0
}

func describeDupError(err error) string {
	hr, ok := com.HR(err)
	if !ok {
		return err.Error()
	}
	switch hr {
	case hrAccessLost:
		return "доступ потерян: смена режима, блокировка экрана или окно UAC"
	case hrAccessDenied:
		return "доступ запрещён: защищённый рабочий стол (блокировка, UAC)"
	case hrNotCurrentlyAvailable:
		return "дублирование сейчас недоступно: превышен лимит дублирований или нет интерактивного рабочего стола"
	case hrUnsupported:
		return "дублирование не поддерживается: на ноутбуке с двумя видеокартами так бывает, когда программа работает не на той видеокарте, к которой подключён монитор; бывает и в сеансе без экрана, например удалённый рабочий стол"
	case hrSessionDisconnected:
		return "сеанс отключён"
	}
	return err.Error()
}

func (c *Capturer) deviceRemoved() bool {
	if c.deviceRemovedFn != nil {
		return c.deviceRemovedFn()
	}
	if c.device == nil {
		return true
	}
	return int32(c.device.Call(slotDeviceGetRemovedReason)) < 0
}

// acquire ждёт новый кадр рабочего стола не дольше ms миллисекунд.
func (c *Capturer) acquire(ms uint32) error {
	var info outduplFrameInfo
	var res *com.Object
	hr := c.dup.Call(slotDupAcquireNextFrame, uintptr(ms), uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&res)))
	if uint32(hr) == hrWaitTimeout {
		return nil
	}
	if err := com.Check("AcquireNextFrame", hr); err != nil {
		c.lost.Add(1)
		c.dup.Release()
		c.dup = nil
		c.dupRetryAt = time.Now().Add(dupRetryInterval)
		if c.deviceRemoved() {
			return ErrDeviceLost
		}
		c.dupErr = err
		c.logf("захват экрана прерван (%v); пересоздаём дублирование", describeDupError(err))
		return nil
	}
	now := time.Now()
	defer func() {
		res.Release()
		c.dup.Call(slotDupReleaseFrame)
	}()
	var convertErr error
	if info.LastPresentTime != 0 || !c.haveDesktop || c.sizeRotation != c.rotation {
		if err := c.copyDesktop(res); err != nil {
			if !errors.Is(err, ErrVideoProcessor) {
				return err
			}
			// A rotated mode may lack VP rotation support. The failed setup
			// dropped the VP and reset srcW/srcH: retain this acquired desktop
			// and its pointer metadata on the CPU path BEFORE asking the source
			// to replace the texture encoder. Its next Grab can be a repeat.
			convertErr = err
			if err := c.copyDesktop(res); err != nil {
				return err
			}
		}
		c.haveDesktop = true
		c.dirty = true
		c.version++
		c.contentAt = now
		c.captured.Add(1)
	}
	if info.LastMouseUpdateTime != 0 {
		vis := info.PointerVisible != 0
		if vis != c.curVisible || int(info.PointerX) != c.curX || int(info.PointerY) != c.curY {
			c.curVisible, c.curX, c.curY = vis, int(info.PointerX), int(info.PointerY)
			if c.cfg.ShowCursor {
				c.dirty = true
				c.version++
				if c.contentAt.IsZero() || now.After(c.contentAt) {
					c.contentAt = now
				}
			}
		}
	}
	if info.PointerShapeBufferLen != 0 && c.cfg.ShowCursor {
		c.fetchShape(int(info.PointerShapeBufferLen))
		c.dirty = true
		c.version++
	}
	return convertErr
}

func (c *Capturer) copyDesktop(res *com.Object) error {
	tex, err := res.QueryInterface(&iidTexture2D)
	if err != nil {
		return fmt.Errorf("текстура кадра рабочего стола: %w", err)
	}
	defer tex.Release()
	var d texture2DDesc
	tex.Call(slotTextureGetDesc, uintptr(unsafe.Pointer(&d)))
	c.frameFormat = d.Format
	if err := checkFrameFormat(d.Format); err != nil {
		// CopyResource в BGRA8-текстуру из другого формата даёт мусор или чёрный
		// кадр: останавливаемся с понятной причиной.
		if !c.warnedFormat {
			c.warnedFormat = true
			c.logf("%v", err)
		}
		return err
	}
	if err := c.ensureSize(int(d.Width), int(d.Height)); err != nil {
		return err
	}
	c.ctx.Call(slotCtxCopyResource, uintptr(unsafe.Pointer(c.desktop)), uintptr(unsafe.Pointer(tex)))
	return nil
}

func (c *Capturer) fetchShape(size int) {
	if size <= 0 || size > 4<<20 {
		return
	}
	buf := make([]byte, size)
	var required uint32
	var si outduplPointerShapeInfo
	hr := c.dup.Call(slotDupGetFramePtrShape, uintptr(size), uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&required)), uintptr(unsafe.Pointer(&si)))
	if com.Check("GetFramePointerShape", hr) != nil {
		return
	}
	s := &CursorShape{
		Type: si.Type, Width: int(si.Width), Height: int(si.Height), Pitch: int(si.Pitch),
		HotX: int(si.HotX), HotY: int(si.HotY), Data: buf,
	}
	if w, h := s.Size(); !s.Valid() || w > maxCursorSideSize || h > maxCursorSideSize {
		c.debugf("форма указателя не поддерживается: тип %d, %dx%d, шаг %d", si.Type, si.Width, si.Height, si.Pitch)
		c.shape = nil
		return
	}
	c.shape = s
	c.surfaceShape = rotateCursorShape(s, inverseRotation(c.sizeRotation))
}

// --- выдача кадров ---

func sleepOrStop(d time.Duration, stop <-chan struct{}) bool {
	if d <= 0 {
		select {
		case <-stop:
			return false
		default:
			return true
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-stop:
		return false
	}
}

// Grab блокируется до момента выдачи очередного кадра (период 1/FPS) и
// возвращает его. Пока на рабочем столе нет новых кадров, возвращается
// предыдущая картинка; при потере дублирования выдача продолжается, а
// дублирование пересоздаётся в промежутках. Исключение — гибридная система, где
// до первого кадра дублирование отвечает DXGI_ERROR_UNSUPPORTED (см. holdFrames):
// кадры не выдаются, пока оно не откроется или не придёт UnsupportedError.
// После закрытия stop возвращает ErrStopped.
func (c *Capturer) Grab(stop <-chan struct{}) (Frame, error) {
	if c.next.IsZero() {
		c.next = time.Now()
	}
	for {
		select {
		case <-stop:
			return Frame{}, ErrStopped
		default:
		}
		now := time.Now()
		if c.dup == nil {
			if err := c.tryOpenDup(now); err != nil {
				return Frame{}, err
			}
		}
		if c.holdFrames() {
			if !sleepOrStop(20*time.Millisecond, stop) {
				return Frame{}, ErrStopped
			}
			continue
		}
		deadline := c.next
		if !c.haveDesktop && c.dup != nil && c.firstDeadline.After(deadline) {
			deadline = c.firstDeadline // первый кадр ждём подольше, чтобы он был не чёрным
		}
		if !now.Before(deadline) {
			return c.produce(now)
		}
		wait := deadline.Sub(now)
		if c.dup == nil {
			if wait > 20*time.Millisecond {
				wait = 20 * time.Millisecond
			}
			if !sleepOrStop(wait, stop) {
				return Frame{}, ErrStopped
			}
			continue
		}
		ms := uint32((wait + time.Millisecond - 1) / time.Millisecond)
		if ms > 50 {
			ms = 50 // чтобы вовремя реагировать на stop
		}
		if err := c.acquire(ms); err != nil {
			return Frame{}, err
		}
	}
}

func (c *Capturer) cursorRegion() (Rect, bool) {
	if !c.cfg.ShowCursor || !c.curVisible || !c.shape.Valid() {
		return Rect{}, false
	}
	r := c.surfaceCursorRect()
	x0, y0 := maxInt(0, r.X), maxInt(0, r.Y)
	x1, y1 := minInt(c.srcW, r.Right()), minInt(c.srcH, r.Bottom())
	if x1 <= x0 || y1 <= y0 {
		return Rect{}, false
	}
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}, true
}

func (c *Capturer) surfaceCursorRect() Rect {
	w, h := c.shape.Size()
	// DXGI PointerPosition is the shape's top-left in upright, output-local
	// desktop coordinates, NOT native surface coordinates or a hotspot.
	// Do not subtract HotX/HotY or the virtual desktop monitor offset.
	return desktopRectToSurface(Rect{X: c.curX, Y: c.curY, W: w, H: h}, c.srcW, c.srcH, c.sizeRotation)
}

func (c *Capturer) produce(now time.Time) (Frame, error) {
	f := Frame{At: now, Fresh: c.dirty}
	if c.dirty && !c.contentAt.IsZero() {
		f.At = c.contentAt
	}
	src, srcView := c.desktop, c.deskView
	if region, ok := c.cursorRegion(); ok {
		if c.composedVersion != c.version {
			c.composeCursor(region)
			c.composedVersion = c.version
		}
		src, srcView = c.composed, c.compView
	}
	switch {
	case c.vp != nil:
		if c.wantTexture || c.dataVersion != c.version || len(c.data) == 0 {
			idx := c.ringPos
			c.ringPos = (c.ringPos + 1) % len(c.ring)
			if err := c.vp.blt(srcView, c.ringViews[idx]); err != nil {
				if ferr := c.onBltFailure(err); ferr != nil {
					return Frame{}, ferr
				}
				return c.produceCPU(f, src)
			}
			if c.wantTexture {
				c.ctx.Call(slotCtxFlush)
				f.Texture = c.ring[idx]
			} else if err := c.readbackNV12(c.ring[idx]); err != nil {
				if c.deviceRemoved() {
					return Frame{}, ErrDeviceLost
				}
				c.logf("чтение NV12 с GPU не удалось (%v): переходим на преобразование на CPU", err)
				c.dropVideoProc(fmt.Sprintf("чтение NV12 с GPU не удалось: %v", err))
				return c.produceCPU(f, src)
			}
			c.dataVersion = c.version
		}
		if !c.wantTexture {
			f.Data = c.data
		}
	default:
		return c.produceCPU(f, src)
	}
	c.finishProduce(now)
	return f, nil
}

func (c *Capturer) finishProduce(now time.Time) {
	c.dirty = false
	c.produced.Add(1)
	c.next = c.next.Add(c.interval)
	if now.After(c.next) {
		c.next = now.Add(c.interval)
	}
}

// produceCPU переводит кадр в NV12 на CPU: копия в staging-текстуру, Map и
// ConvertBGRAToNV12.
func (c *Capturer) produceCPU(f Frame, src *com.Object) (Frame, error) {
	now := time.Now()
	if c.wantTexture {
		return Frame{}, errors.New("текстуры NV12 недоступны без видеопроцессора GPU")
	}
	if c.dataVersion != c.version || len(c.data) == 0 {
		if c.stagingFull == nil {
			t, err := createTexture2D(c.device, &texture2DDesc{
				Width: uint32(c.srcW), Height: uint32(c.srcH), MipLevels: 1, ArraySize: 1,
				Format: dxgiFormatB8G8R8A8UNorm, SampleCount: 1,
				Usage: d3d11UsageStaging, CPUAccessFlags: d3d11CPUAccessRead,
			})
			if err != nil {
				return Frame{}, fmt.Errorf("staging-текстура: %w", err)
			}
			c.stagingFull = t
		}
		c.ctx.Call(slotCtxCopyResource, uintptr(unsafe.Pointer(c.stagingFull)), uintptr(unsafe.Pointer(src)))
		var m mappedSubresource
		hr := c.ctx.Call(slotCtxMap, uintptr(unsafe.Pointer(c.stagingFull)), 0, d3d11MapRead, 0, uintptr(unsafe.Pointer(&m)))
		if err := com.Check("Map(staging BGRA)", hr); err != nil {
			if c.deviceRemoved() {
				return Frame{}, ErrDeviceLost
			}
			return Frame{}, err
		}
		pix := unsafe.Slice((*byte)(m.Data), int(m.RowPitch)*c.srcH)
		if len(c.data) != NV12Size(c.cfg.Width, c.cfg.Height) {
			c.data = make([]byte, NV12Size(c.cfg.Width, c.cfg.Height))
		}
		convertRotatedBGRAToNV12(c.data, c.cfg.Width, c.cfg.Height, pix, int(m.RowPitch), c.srcW, c.srcH, c.fit, c.sizeRotation)
		c.ctx.Call(slotCtxUnmap, uintptr(unsafe.Pointer(c.stagingFull)), 0)
		c.dataVersion = c.version
	}
	f.Data = c.data
	c.finishProduce(now)
	return f, nil
}

// readbackNV12 копирует NV12-текстуру в память (строки без выравнивания).
func (c *Capturer) readbackNV12(tex *com.Object) error {
	w, h := c.cfg.Width, c.cfg.Height
	if c.stagingNV12 == nil {
		t, err := createTexture2D(c.device, &texture2DDesc{
			Width: uint32(w), Height: uint32(h), MipLevels: 1, ArraySize: 1,
			Format: dxgiFormatNV12, SampleCount: 1,
			Usage: d3d11UsageStaging, CPUAccessFlags: d3d11CPUAccessRead,
		})
		if err != nil {
			return fmt.Errorf("staging-текстура NV12: %w", err)
		}
		c.stagingNV12 = t
	}
	c.ctx.Call(slotCtxCopyResource, uintptr(unsafe.Pointer(c.stagingNV12)), uintptr(unsafe.Pointer(tex)))
	var m mappedSubresource
	hr := c.ctx.Call(slotCtxMap, uintptr(unsafe.Pointer(c.stagingNV12)), 0, d3d11MapRead, 0, uintptr(unsafe.Pointer(&m)))
	if err := com.Check("Map(staging NV12)", hr); err != nil {
		return err
	}
	defer c.ctx.Call(slotCtxUnmap, uintptr(unsafe.Pointer(c.stagingNV12)), 0)
	pitch := int(m.RowPitch)
	if pitch < w {
		return fmt.Errorf("шаг строки NV12 %d меньше ширины %d", pitch, w)
	}
	mem := unsafe.Slice((*byte)(m.Data), pitch*(h+h/2))
	if len(c.data) != NV12Size(w, h) {
		c.data = make([]byte, NV12Size(w, h))
	}
	for r := 0; r < h; r++ {
		copy(c.data[r*w:(r+1)*w], mem[r*pitch:r*pitch+w])
	}
	uv := pitch * h
	for r := 0; r < h/2; r++ {
		copy(c.data[w*h+r*w:w*h+(r+1)*w], mem[uv+r*pitch:uv+r*pitch+w])
	}
	return nil
}

// composeCursor копирует рабочий стол в composed и рисует указатель: участок
// под указателем читается на CPU, смешивается и записывается обратно.
func (c *Capturer) composeCursor(region Rect) {
	c.ctx.Call(slotCtxCopyResource, uintptr(unsafe.Pointer(c.composed)), uintptr(unsafe.Pointer(c.desktop)))
	if c.stagingCursor == nil || c.stagingCurW < region.W || c.stagingCurH < region.H {
		c.stagingCursor.Release()
		c.stagingCursor = nil
		w, h := c.shape.Size()
		w, h = maxInt(w, region.W), maxInt(h, region.H)
		t, err := createTexture2D(c.device, &texture2DDesc{
			Width: uint32(w), Height: uint32(h), MipLevels: 1, ArraySize: 1,
			Format: dxgiFormatB8G8R8A8UNorm, SampleCount: 1,
			Usage: d3d11UsageStaging, CPUAccessFlags: d3d11CPUAccessRead,
		})
		if err != nil {
			c.debugf("staging-текстура указателя: %v", err)
			return
		}
		c.stagingCursor, c.stagingCurW, c.stagingCurH = t, w, h
	}
	box := box32{Left: uint32(region.X), Top: uint32(region.Y), Right: uint32(region.Right()), Bottom: uint32(region.Bottom()), Back: 1}
	c.ctx.Call(slotCtxCopySubresourceRegn, uintptr(unsafe.Pointer(c.stagingCursor)), 0, 0, 0, 0,
		uintptr(unsafe.Pointer(c.composed)), 0, uintptr(unsafe.Pointer(&box)))
	var m mappedSubresource
	hr := c.ctx.Call(slotCtxMap, uintptr(unsafe.Pointer(c.stagingCursor)), 0, d3d11MapRead, 0, uintptr(unsafe.Pointer(&m)))
	if com.Check("Map(указатель)", hr) != nil {
		return
	}
	stride := region.W * 4
	buf := make([]byte, stride*region.H)
	mem := unsafe.Slice((*byte)(m.Data), int(m.RowPitch)*region.H)
	for r := 0; r < region.H; r++ {
		copy(buf[r*stride:(r+1)*stride], mem[r*int(m.RowPitch):r*int(m.RowPitch)+stride])
	}
	c.ctx.Call(slotCtxUnmap, uintptr(unsafe.Pointer(c.stagingCursor)), 0)
	r := c.surfaceCursorRect()
	BlendCursor(buf, stride, region.W, region.H, r.X-region.X, r.Y-region.Y, c.surfaceShape)
	c.ctx.Call(slotCtxUpdateSubresource, uintptr(unsafe.Pointer(c.composed)), 0, uintptr(unsafe.Pointer(&box)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(stride), 0)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (c *Capturer) createDesktopTextures(bind uint32) error {
	desc := &texture2DDesc{
		Width: uint32(c.srcW), Height: uint32(c.srcH), MipLevels: 1, ArraySize: 1,
		Format: dxgiFormatB8G8R8A8UNorm, SampleCount: 1,
		Usage: d3d11UsageDefault, BindFlags: bind,
	}
	var err error
	if c.desktop, err = createTexture2D(c.device, desc); err != nil {
		return fmt.Errorf("текстура рабочего стола: %w", err)
	}
	if c.composed, err = createTexture2D(c.device, desc); err != nil {
		return fmt.Errorf("текстура кадра с указателем: %w", err)
	}
	return nil
}

// releaseTexturesOnly освобождает текстуры рабочего стола и их представления
// для повторной попытки с другими флагами привязки.
func (c *Capturer) releaseTexturesOnly() {
	for _, o := range []**com.Object{&c.deskView, &c.compView, &c.desktop, &c.composed} {
		(*o).Release()
		*o = nil
	}
}

// setPath запоминает выбранный путь преобразования цвета и причину (для diag).
func (c *Capturer) setPath(gpu bool, reason string) {
	c.gpuPath, c.pathReason = gpu, reason
}

// dropVideoProc отключает видеопроцессор GPU: дальше преобразование идёт на CPU.
func (c *Capturer) dropVideoProc(reason string) {
	if c.vp != nil {
		c.vp.release()
		c.vp = nil
	}
	c.releaseRing()
	c.setPath(false, reason)
}

// onBltFailure решает, что делать с ошибкой videoProc.blt. Устройство потеряно —
// ErrDeviceLost. Кадры в память (!wantTexture) — видеопроцессор отключается и
// возвращается nil: вызывающий выдаёт этот кадр с преобразованием на CPU.
// Кадры текстурой — особая ошибка VideoProcError: кодеру нужна системная
// память, источнику следует вызвать SetWantTexture(false) и пересоздать кодер
// (следующий Grab сам перейдёт на CPU, если видеопроцессор откажет снова).
func (c *Capturer) onBltFailure(err error) error {
	if c.deviceRemoved() {
		return ErrDeviceLost
	}
	if c.wantTexture {
		return &VideoProcError{Err: err}
	}
	c.logf("видеопроцессор GPU не справился с кадром (%v): переходим на преобразование на CPU", err)
	c.dropVideoProc(fmt.Sprintf("ошибка VideoProcessorBlt: %v", err))
	return nil
}

// ConvertPath сообщает, как преобразуется цвет кадров: на GPU (видеопроцессор)
// или на CPU, и почему выбран этот путь (для diag и журнала).
func (c *Capturer) ConvertPath() (gpu bool, reason string) { return c.vp != nil, c.pathReason }

// DeviceVideoSupport сообщает, создано ли устройство D3D11 с поддержкой видео;
// если нет, возвращает исходный HRESULT попытки с VIDEO_SUPPORT.
func (c *Capturer) DeviceVideoSupport() (ok bool, hr uint32) {
	return !c.devInfo.NoVideo, c.devInfo.VideoHR
}

// FrameFormat возвращает DXGI_FORMAT последнего полученного кадра рабочего
// стола (0 — кадров ещё не было).
func (c *Capturer) FrameFormat() uint32 { return c.frameFormat }

// MonitorName возвращает имя монитора (\.\DISPLAYn), на котором открыт захват.
func (c *Capturer) MonitorName() string { return c.monitor }

// --- ошибки захвата ---

// ErrVideoProcessor — видеопроцессор GPU отказал при кадрах, запрошенных
// текстурой: источнику нужно перейти на системную память.
var ErrVideoProcessor = errors.New("видеопроцессор GPU не смог преобразовать кадр")

// VideoProcError — ErrVideoProcessor с исходной ошибкой (HRESULT) для журнала.
type VideoProcError struct{ Err error }

func (e *VideoProcError) Error() string { return fmt.Sprintf("%v: %v", ErrVideoProcessor, e.Err) }
func (e *VideoProcError) Unwrap() error { return e.Err }
func (e *VideoProcError) Is(target error) bool {
	return target == ErrVideoProcessor
}

// ErrCaptureStale — после первого кадра нет новых: дублирование не открывается
// (UNSUPPORTED/NOT_FOUND) дольше staleAfter. Захват нужно создать заново.
var ErrCaptureStale = errors.New("захват экрана остановился")

// StaleError — ErrCaptureStale с подробностями. Совпадает и с ErrDeviceLost:
// «источник нужно создать заново».
type StaleError struct {
	Adapter string
	Monitor string
	Since   time.Duration // сколько нет новых кадров
	Err     error         // последняя ошибка (с HRESULT)
}

func (e *StaleError) Error() string {
	return fmt.Sprintf("%v: нет новых кадров %v (монитор %s на %s): %v; захват будет создан заново",
		ErrCaptureStale, e.Since.Round(time.Second), e.Monitor, e.Adapter, e.Err)
}
func (e *StaleError) Unwrap() error { return e.Err }
func (e *StaleError) Is(target error) bool {
	return target == ErrCaptureStale || target == ErrDeviceLost
}

// ErrFrameFormat — формат кадра рабочего стола не BGRA8 (включён HDR).
var ErrFrameFormat = errors.New("формат кадра рабочего стола не поддерживается")

// FrameFormatError — ErrFrameFormat с названием формата.
type FrameFormatError struct{ Format uint32 }

func (e *FrameFormatError) Error() string {
	return fmt.Sprintf("%v: %s (нужен BGRA8). Выключите HDR на этом мониторе: Параметры Windows -> Система -> Дисплей -> HDR",
		ErrFrameFormat, FormatName(e.Format))
}
func (e *FrameFormatError) Is(target error) bool { return target == ErrFrameFormat }

// checkFrameFormat возвращает FrameFormatError для любого формата, кроме BGRA8.
func checkFrameFormat(format uint32) error {
	if format == dxgiFormatB8G8R8A8UNorm {
		return nil
	}
	return &FrameFormatError{Format: format}
}

// FormatName — читаемое название DXGI_FORMAT кадра рабочего стола.
func FormatName(f uint32) string {
	switch f {
	case 87:
		return "B8G8R8A8_UNORM"
	case 10:
		return "R16G16B16A16_FLOAT (HDR)"
	case 24:
		return "R10G10B10A2_UNORM (10 бит/HDR)"
	case 28:
		return "R8G8B8A8_UNORM"
	case 0:
		return "нет кадров"
	}
	return fmt.Sprintf("DXGI_FORMAT %d", f)
}
