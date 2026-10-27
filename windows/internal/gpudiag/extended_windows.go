//go:build windows && (amd64 || arm64)

package gpudiag

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"airspoon/internal/airplay"
	"airspoon/internal/capture/dxgi"
	"airspoon/internal/encode/ffmpeg"
	"airspoon/internal/encode/mf"
	"airspoon/internal/win/com"
)

// Расширенная диагностика (план windows-devices, A12): всё, что нужно знать про
// машину за один запуск, без выезда к человеку. Собирается при Options.Extended.

const (
	defaultEncodeProbe = 2 * time.Second
	defaultMeasure     = 5 * time.Second
	// trialMaxSkipPercent — больше стольких процентов пропущенных кадров проба
	// считается проваленной (тот же порог, что у строки о проблемах в журнале).
	trialMaxSkipPercent = 10
)

// trialProfile — размер и частота пробного кодирования.
type trialProfile struct {
	W, H, FPS int
}

func (p trialProfile) String() string { return fmt.Sprintf("%dp%d", p.H, p.FPS) }

var trialProfiles = []trialProfile{{1280, 720, 30}, {1920, 1080, 30}, {1920, 1080, 60}}

// EncodeTrial — одно пробное кодирование.
type EncodeTrial struct {
	Adapter     int // индекс видеокарты; -1 — программный кодер
	AdapterName string
	Encoder     string
	Hardware    bool
	Profile     trialProfile
	Report      mf.ProbeReport
	// Err — кодер не запустился или проба не состоялась (Report тогда пуст).
	Err string
}

// Passed — проба прошла: кодер выдал пакеты и пропустил не больше 10% кадров.
func (t EncodeTrial) Passed() bool {
	r := t.Report
	return t.Err == "" && r.Err == "" && r.Offered > 0 && r.Packets > 0 &&
		r.Skipped*100 <= r.Offered*trialMaxSkipPercent
}

// Input — какой вход сработал в пробе.
func (t EncodeTrial) Input() string { return t.Report.Input }

func (t EncodeTrial) textures() bool { return strings.Contains(t.Report.Input, "текстур") }

// AdapterEncoderList — кодеры одной видеокарты с флагами.
type AdapterEncoderList struct {
	Index int
	Name  string
	Infos []mf.EncoderInfo
	Err   error
}

// NetworkInfo — интерфейс маршрута к приёмнику.
type NetworkInfo struct {
	Target string // с чем сверялся маршрут
	Peer   bool   // Target — настоящий приёмник, а не адрес «наружу»
	Route  airplay.RouteInterface
	Err    error
}

// AudioInfo — устройство вывода звука по умолчанию.
type AudioInfo struct {
	Out airplay.DefaultAudioOutput
	Err error
}

// Extended — результаты, которые собираются только в расширенном режиме.
type Extended struct {
	AdapterProbes    []dxgi.AdapterProbe
	AdapterProbesErr error
	Monitors         []dxgi.MonitorProbe
	MonitorsErr      error
	// MonitorsMeasured — захват мониторов реально открывался и измерялся.
	MonitorsMeasured bool
	EncoderLists     []AdapterEncoderList
	Trials           []EncodeTrial
	// TrialsSkipped — почему пробное кодирование не выполнялось (пусто — выполнялось).
	TrialsSkipped  string
	Network        NetworkInfo
	Audio          AudioInfo
	Recommendation string
}

func (o Options) encodeProbe() time.Duration {
	if o.EncodeProbe == 0 {
		return defaultEncodeProbe
	}
	return o.EncodeProbe
}

func (o Options) measure() time.Duration {
	if o.MeasureCapture == 0 {
		return defaultMeasure
	}
	return o.MeasureCapture
}

func (o Options) progress(format string, args ...any) {
	if o.Progress != nil {
		o.Progress(fmt.Sprintf(format, args...))
	}
}

// collectExtended заполняет r.Ext. Вызывается из потока с com.Init.
func (r *Report) collectExtended(opts Options) {
	ext := &Extended{}
	r.Ext = ext

	opts.progress("видеокарты...")
	ext.AdapterProbes, ext.AdapterProbesErr = dxgi.ProbeAdapters()

	if m := opts.measure(); m > 0 {
		opts.progress("захват экрана (%v на каждый монитор)...", m)
		ext.Monitors, ext.MonitorsErr = dxgi.ProbeMonitors(m)
		ext.MonitorsMeasured = ext.MonitorsErr == nil
	}

	switch {
	case opts.encodeProbe() < 0:
		ext.TrialsSkipped = "пропущено по просьбе (идёт показ экрана или отключено флагом)"
	case r.MFErr != nil:
		ext.TrialsSkipped = "Media Foundation недоступна"
	default:
		r.runTrials(opts, ext)
	}

	opts.progress("сеть и звук...")
	ext.Network = collectNetwork(opts.Peer)
	ext.Audio.Out, ext.Audio.Err = airplay.DefaultAudioOutputInfo()
	ext.Recommendation = recommend(r)
}

func collectNetwork(peer string) NetworkInfo {
	n := NetworkInfo{Target: "8.8.8.8 (маршрут по умолчанию; приёмник не указан)"}
	host := "8.8.8.8"
	if peer != "" {
		host = peer
		if h, _, err := net.SplitHostPort(peer); err == nil {
			host = h
		}
		n.Target, n.Peer = host, true
	}
	n.Route, n.Err = airplay.RouteInterfaceTo(host)
	return n
}

// ownerLUID — видеокарта, которой принадлежит монитор display (nil — не найден).
func ownerLUID(display int) *dxgi.LUID {
	outs, err := dxgi.ListOutputs()
	if err != nil {
		return nil
	}
	for _, o := range outs {
		if o.Index == display {
			l := o.AdapterLUID
			return &l
		}
	}
	return nil
}

func (r *Report) runTrials(opts Options, ext *Extended) {
	dur := opts.encodeProbe()
	owner := ownerLUID(opts.Display)
	frames := func(p trialProfile) int { return int(dur.Seconds()*float64(p.FPS) + 0.5) }
	cfgFor := func(p trialProfile) mf.ProbeConfig {
		return mf.ProbeConfig{Width: p.W, Height: p.H, FPS: p.FPS, BitrateKbps: ffmpeg.AutoBitrateKbps(p.W, p.H, p.FPS), Frames: frames(p)}
	}

	for i, a := range r.Adapters {
		if a.Software() {
			continue
		}
		if isDuplicateAdapter(r.Adapters, i) {
			// В сеансе удалённого рабочего стола одна карта перечисляется дважды под
			// разными LUID: вторую копию без мониторов не пробуем.
			continue
		}
		l := mf.AdapterLUID{Low: a.LUID.Low, High: a.LUID.High}
		infos, err := mf.ListH264Encoders(&l)
		ext.EncoderLists = append(ext.EncoderLists, AdapterEncoderList{Index: a.Index, Name: a.Name, Infos: infos, Err: err})
		if err != nil {
			continue
		}
		ownsMonitor := owner != nil && *owner == a.LUID
		vpOK := adapterVideoProcOK(ext.AdapterProbes, a.LUID)
		for _, info := range infos {
			if info.Err != "" {
				continue
			}
			for _, p := range trialProfiles {
				opts.progress("кодер %q на %s, %s, вход через память...", info.Name, a.Name, p)
				pc := cfgFor(p)
				pc.EncoderName, pc.AdapterLUID = info.Name, &l
				ext.Trials = append(ext.Trials, runTrial(a.Index, a.Name, info.Name, true, p, func() (mf.ProbeReport, error) { return mf.ProbeEncode(pc) }))
			}
			// Вход текстурами проверяется на видеокарте, которая захватывает экран:
			// текстуры кадра принадлежат её устройству.
			if ownsMonitor && vpOK && info.D3D11Aware {
				for _, p := range trialProfiles {
					opts.progress("кодер %q на %s, %s, вход текстурами GPU...", info.Name, a.Name, p)
					pc := cfgFor(p)
					pc.EncoderName, pc.AdapterLUID = info.Name, &l
					ext.Trials = append(ext.Trials, runTrial(a.Index, a.Name, info.Name, true, p, func() (mf.ProbeReport, error) {
						return textureProbe(opts.Display, pc, dur)
					}))
				}
			}
		}
	}

	sw := mf.SoftwareEncoderInfo()
	if sw.Err == "" {
		for _, p := range trialProfiles {
			opts.progress("программный кодер, %s...", p)
			pc := cfgFor(p)
			pc.Software = true
			ext.Trials = append(ext.Trials, runTrial(-1, "процессор", sw.Name, false, p, func() (mf.ProbeReport, error) { return mf.ProbeEncode(pc) }))
		}
	}
}

// isDuplicateAdapter — адаптер i без мониторов повторяет раньше перечисленный
// (те же vendor, device и название).
func isDuplicateAdapter(list []dxgi.AdapterInfo, i int) bool {
	a := list[i]
	if len(a.Outputs) > 0 {
		return false
	}
	for _, b := range list[:i] {
		if b.VendorID == a.VendorID && b.DeviceID == a.DeviceID && b.Name == a.Name {
			return true
		}
	}
	return false
}

// adapterVideoProcOK —видеопроцессор этой видеокарты умеет BGRA -> NV12.
func adapterVideoProcOK(probes []dxgi.AdapterProbe, luid dxgi.LUID) bool {
	for _, p := range probes {
		if p.LUID == luid {
			return p.VideoProc.BGRAToNV12
		}
	}
	return false
}

// runTrial выполняет пробу, не позволяя панике или сбою драйвера оборвать отчёт.
func runTrial(adapter int, adapterName, encoder string, hardware bool, p trialProfile, run func() (mf.ProbeReport, error)) (t EncodeTrial) {
	t = EncodeTrial{Adapter: adapter, AdapterName: adapterName, Encoder: encoder, Hardware: hardware, Profile: p}
	defer func() {
		if rec := recover(); rec != nil {
			t.Err = fmt.Sprintf("авария пробы: %v", rec)
		}
	}()
	rep, err := run()
	t.Report = rep
	if err != nil {
		t.Err = err.Error()
	}
	return t
}

var errNoVideoProc = errors.New("видеопроцессор GPU недоступен: кадры-текстуры получить нельзя")

// textureProbe кодирует настоящие кадры экрана, переданные кодеру текстурами GPU.
func textureProbe(display int, pc mf.ProbeConfig, dur time.Duration) (mf.ProbeReport, error) {
	capt, err := dxgi.New(dxgi.Config{Display: display, Width: pc.Width, Height: pc.Height, FPS: pc.FPS, WantTexture: true})
	if err != nil {
		return mf.ProbeReport{}, fmt.Errorf("захват экрана: %w", err)
	}
	defer capt.Close()
	if !capt.HasVideoProcessor() {
		return mf.ProbeReport{}, errNoVideoProc
	}
	stop := make(chan struct{})
	timer := time.AfterFunc(dur+10*time.Second, func() { close(stop) })
	defer timer.Stop()
	pc.Device = capt.Device()
	pc.MakeTexture = func(int) (*com.Object, func(), error) {
		f, err := capt.Grab(stop)
		if err != nil {
			return nil, nil, err
		}
		if f.Texture == nil {
			return nil, nil, errors.New("захват не выдал текстуру")
		}
		return f.Texture, nil, nil
	}
	return mf.ProbeEncode(pc)
}

// --- итоговая рекомендация ---

// profileRank — число, растущее с нагрузкой профиля (для сравнения).
func profileRank(p trialProfile) int { return p.H*1000 + p.FPS }

// bestTrial — лучший прошедший вариант среди проб подходящего класса: самый
// тяжёлый профиль не выше maxProfile; при равных профилях вход текстурами лучше
// входа через память.
func bestTrial(trials []EncodeTrial, hardware bool, maxProfile trialProfile) (EncodeTrial, bool) {
	var best EncodeTrial
	found := false
	for _, t := range trials {
		if t.Hardware != hardware || !t.Passed() || profileRank(t.Profile) > profileRank(maxProfile) {
			continue
		}
		better := !found ||
			profileRank(t.Profile) > profileRank(best.Profile) ||
			(profileRank(t.Profile) == profileRank(best.Profile) && t.textures() && !best.textures())
		if better {
			best, found = t, true
		}
	}
	return best, found
}

// softwareCap — потолок профиля программного кодера (как в cmd/airspoon: 720p30,
// при шести и более логических ядрах 1080p30).
func softwareCap(cores int) trialProfile {
	if cores >= 6 {
		return trialProfiles[1]
	}
	return trialProfiles[0]
}

// recommend — итог одной строкой: какой кодер, на какой карте, какой вход и
// какой профиль будут на этой машине.
func recommend(r *Report) string {
	ext := r.Ext
	if ext == nil {
		return ""
	}
	if r.MFErr != nil {
		return "показ невозможен: Media Foundation недоступна (" + r.MFErr.Error() + "); " + mfAdvice(r.System.IsN)
	}
	for _, m := range ext.Monitors {
		if (m.Primary || len(ext.Monitors) == 1) && m.Err != nil {
			return "захват экрана не работает: " + m.Err.Error()
		}
	}
	if ext.TrialsSkipped != "" {
		return "пробное кодирование не выполнялось (" + ext.TrialsSkipped + "), рекомендацию по кодерам дать нельзя"
	}
	hw, hwOK := bestTrial(ext.Trials, true, trialProfiles[2])
	sw, swOK := bestTrial(ext.Trials, false, softwareCap(r.System.LogicalCores))
	switch {
	case hwOK:
		return fmt.Sprintf("на этой машине будет аппаратный кодер %q на карте %s, вход: %s, профиль %s",
			hw.Encoder, hw.AdapterName, hw.Input(), hw.Profile)
	case swOK:
		why := "аппаратные кодеры не прошли пробу"
		if !anyEncoder(ext.EncoderLists) {
			why = "аппаратных кодеров нет"
		}
		return fmt.Sprintf("на этой машине будет программный кодер %q на процессоре (%s), вход: %s, профиль %s (качество снижено)",
			sw.Encoder, why, sw.Input(), sw.Profile)
	}
	return "ни один кодер не прошёл пробу: показ экрана, скорее всего, не заработает (см. строки проб выше)"
}

func anyEncoder(lists []AdapterEncoderList) bool {
	for _, l := range lists {
		for _, i := range l.Infos {
			if i.Err == "" {
				return true
			}
		}
	}
	return false
}

func mfAdvice(isN bool) string {
	if isN {
		return "редакция N/KN: установите «Пакет мультимедийных функций» (Media Feature Pack)"
	}
	return "проверьте, что Windows 10 1809 или новее и установлены компоненты мультимедиа"
}

// --- строки отчёта ---

func hrText(hr uint32) string {
	if hr == 0 {
		return "S_OK"
	}
	return fmt.Sprintf("0x%08X", hr)
}

func attemptText(a dxgi.CreateAttempt) string {
	if !a.Tried {
		return "не пробовали"
	}
	if a.OK {
		return fmt.Sprintf("успешно (уровень %s)", dxgi.FeatureLevelName(a.FeatureLevel))
	}
	return "ошибка " + hrText(a.HR)
}

// adapterProbeLines — строки пробы одной видеокарты (с отступом под её строкой).
func adapterProbeLines(p dxgi.AdapterProbe) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf("      "+format, args...)) }
	kind := ""
	if p.BasicDisplay {
		kind = "; это Microsoft Basic Display/Render Driver: драйвер видеокарты не установлен или не работает"
	}
	drv := "драйвер не прочитан"
	if p.UMDOK {
		drv = "драйвер " + p.UMDText
	} else if p.UMDHR != 0 {
		drv = "драйвер не прочитан (" + hrText(p.UMDHR) + ")"
	}
	add("%s%s", drv, kind)
	add("D3D11CreateDevice с поддержкой видео: %s; без неё: %s", attemptText(p.WithVideo), attemptText(p.WithoutVideo))
	vp := "нет"
	if p.HasVideoDevice {
		vp = "да"
	}
	add("видеоустройство (ID3D11VideoDevice): %s", vp)
	switch {
	case !p.VideoProc.Checked:
		add("видеопроцессор BGRA->NV12: не проверялся (нет видеоустройства)")
	case p.VideoProc.BGRAToNV12:
		add("видеопроцессор BGRA->NV12: поддерживается (вход %s, выход %s)", hrText(p.VideoProc.InHR), hrText(p.VideoProc.OutHR))
	default:
		reason := ""
		if p.VideoProc.Err != nil {
			reason = ": " + p.VideoProc.Err.Error()
		}
		add("видеопроцессор BGRA->NV12: НЕ поддерживается (вход %s флаги 0x%X, выход %s флаги 0x%X)%s",
			hrText(p.VideoProc.InHR), p.VideoProc.InFlags, hrText(p.VideoProc.OutHR), p.VideoProc.OutFlags, reason)
	}
	return out
}

func rotationName(r uint32) string {
	switch r {
	case 0, 1:
		return "без поворота"
	case 2:
		return "90°"
	case 3:
		return "180°"
	case 4:
		return "270°"
	}
	return fmt.Sprintf("код %d", r)
}

// monitorLines — строки пробы захвата монитора.
func monitorLines(m dxgi.MonitorProbe, measured bool) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf("  "+format, args...)) }
	prim := ""
	if m.Primary {
		prim = ", основной"
	}
	add("монитор [%d] %s на карте «%s»%s: %dx%d, поворот: %s", m.Display, m.Name, m.Adapter, prim, m.Bounds.W, m.Bounds.H, rotationName(m.Rotation))
	cs := "цветовое пространство не определено"
	if m.ColorSpaceKnown {
		cs = "цветовое пространство " + dxgi.ColorSpaceName(m.ColorSpace)
		if m.HDR {
			cs += " — HDR ВКЛЮЧЁН (показ экрана с HDR не поддерживается, выключите HDR)"
		}
		if m.BitsPerColor > 0 {
			cs += fmt.Sprintf(", %d бит на канал", m.BitsPerColor)
		}
	}
	hz := "частота обновления не определена"
	if m.RefreshHz > 0 {
		hz = fmt.Sprintf("частота обновления %d Гц", m.RefreshHz)
	}
	add("    %s; %s", cs, hz)
	if !measured {
		return out
	}
	if m.Err != nil {
		add("    захват: ОШИБКА: %v", m.Err)
	}
	if m.Frames > 0 || m.FrameFormat != 0 {
		add("    формат первого кадра: %s", m.FrameFormatName)
	}
	path := "преобразование цвета на CPU"
	if m.GPUConvert {
		path = "преобразование цвета видеопроцессором GPU"
	}
	if m.PathReason != "" {
		path += " (" + m.PathReason + ")"
	}
	add("    путь: %s", path)
	if m.Frames > 0 {
		repeats := 100 * (m.Frames - m.FreshFrames) / m.Frames
		add("    замер %v (1280x720, 30 к/с): выдано кадров %d, новых %d, повторов %d%%, интервал средний %v, наибольший %v",
			m.Measured.Round(100*time.Millisecond), m.Frames, m.FreshFrames, repeats,
			m.AvgInterval.Round(time.Millisecond), m.MaxInterval.Round(time.Millisecond))
	}
	return out
}

func (t EncodeTrial) lines() []string {
	where := "на карте " + t.AdapterName
	if !t.Hardware {
		where = "на процессоре"
	}
	head := fmt.Sprintf("  %s %q %s:", t.Profile, t.Encoder, where)
	if t.Err != "" {
		return []string{head + " проба не состоялась: " + t.Err}
	}
	verdict := "ПРОШЛА"
	if !t.Passed() {
		verdict = "НЕ ПРОШЛА"
	}
	out := []string{fmt.Sprintf("%s %s. %s", head, verdict, t.Report.Summary())}
	// Отвергнутые параметры уже перечислены в Summary («не принято: ...», с HRESULT);
	// отдельными строками они нужны, только если итоговых параметров прочитать не удалось.
	if t.Report.Settings == "" {
		for _, rj := range t.Report.Rejected {
			how := fmt.Sprintf("SetValue вернул %s", hrText(rj.HR))
			if rj.HR == 0 {
				how = "IsSupported ответил «нет»"
			}
			out = append(out, fmt.Sprintf("      параметр кодера %s отвергнут (%s)", rj.Name, how))
		}
	}
	return out
}

func (e *Extended) trialLines() []string {
	if e.TrialsSkipped != "" {
		return []string{"Пробное кодирование: не выполнялось: " + e.TrialsSkipped}
	}
	out := []string{"Пробное кодирование (кодер, вход, пропуски, время кадра, отвергнутые параметры, итоговые параметры, SPS):"}
	for _, l := range e.EncoderLists {
		if l.Err != nil {
			out = append(out, fmt.Sprintf("  карта [%d] %s: кодеры не перечислены: %v", l.Index, l.Name, l.Err))
			continue
		}
		for _, i := range l.Infos {
			out = append(out, fmt.Sprintf("  карта [%d] %s: %s", l.Index, l.Name, i))
		}
	}
	if len(e.Trials) == 0 {
		out = append(out, "  проб нет: ни один кодер не запустился")
	}
	for _, t := range e.Trials {
		out = append(out, t.lines()...)
	}
	return out
}

func (n NetworkInfo) line() string {
	if n.Err != nil {
		return fmt.Sprintf("Сеть: маршрут к %s не определён: %v", n.Target, n.Err)
	}
	r := n.Route
	kind := map[airplay.NetworkKind]string{
		airplay.NetworkWiFi:    "Wi-Fi (запасы задержки увеличены)",
		airplay.NetworkWired:   "кабель",
		airplay.NetworkOther:   "другой (VPN, виртуальный или мобильный)",
		airplay.NetworkUnknown: "не определён",
	}[r.Kind]
	if kind == "" {
		kind = string(r.Kind)
	}
	s := fmt.Sprintf("Сеть: к %s идёт через «%s» (%s), тип: %s, IfType %d, локальный адрес %v",
		n.Target, r.Name, r.Description, kind, r.IfType, r.LocalIP)
	if r.LinkMbps > 0 {
		s += fmt.Sprintf(", скорость канала %d Мбит/с", r.LinkMbps)
	}
	return s
}

func (a AudioInfo) line() string {
	switch {
	case a.Err != nil:
		return "Звук: устройство вывода не опрошено: " + a.Err.Error()
	case !a.Out.Present:
		return "Звук: включённого устройства вывода нет (звук пойдёт тишиной, пока оно не появится)"
	case a.Out.MixFormatErr != "":
		return fmt.Sprintf("Звук: устройство вывода по умолчанию «%s», формат не прочитан: %s", a.Out.Name, a.Out.MixFormatErr)
	}
	return fmt.Sprintf("Звук: устройство вывода по умолчанию «%s», формат общего режима: %s", a.Out.Name, a.Out.MixFormat)
}
