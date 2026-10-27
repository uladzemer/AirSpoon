package gpupref

import (
	"errors"
	"strings"
	"testing"
)

// memStore — подделка реестра.
type memStore struct {
	m       map[string]string
	setErr  error
	getErr  error
	setCall int
}

func newMem() *memStore { return &memStore{m: map[string]string{}} }

func (s *memStore) Get(exe string) (string, bool, error) {
	if s.getErr != nil {
		return "", false, s.getErr
	}
	for k, v := range s.m {
		if strings.EqualFold(k, exe) {
			return v, true, nil
		}
	}
	return "", false, nil
}

func (s *memStore) Set(exe, data string) error {
	s.setCall++
	if s.setErr != nil {
		return s.setErr
	}
	s.m[exe] = data
	return nil
}

var (
	intel   = Adapter{Name: "Intel(R) UHD Graphics", VendorID: 0x8086, VideoMemory: 128 << 20, Outputs: 1}
	nvidia  = Adapter{Name: "NVIDIA GeForce RTX 3060 Laptop GPU", VendorID: 0x10DE, VideoMemory: 6 << 30}
	basic   = Adapter{Name: "Microsoft Basic Render Driver", VendorID: 0x1414, Software: true}
	rdp     = Adapter{Name: "Microsoft Remote Display Adapter", VendorID: 0x1414}
	oneGPU  = []Adapter{nvidia, rdp}
	hybrid  = []Adapter{nvidia, intel, basic}
	exePath = `C:\Program Files\AirSpoon\AirSpoonSender.exe`
)

func TestParseAndFormatData(t *testing.T) {
	cases := []struct {
		in   string
		want Preference
		ok   bool
	}{
		{"GpuPreference=1;", PowerSaving, true},
		{"GpuPreference=2;", HighPerformance, true},
		{"GpuPreference=0;", Unspecified, true},
		{"AutoHDREnable=0;GpuPreference=1;", PowerSaving, true},
		{" gpupreference = 2 ", HighPerformance, true},
		{"GpuPreference=7;", 0, false},
		{"GpuPreference=;", 0, false},
		{"AutoHDREnable=1;", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseData(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseData(%q) = %v,%v; ожидали %v,%v", c.in, got, ok, c.want, c.ok)
		}
	}
	if got := FormatData("", PowerSaving); got != "GpuPreference=1;" {
		t.Errorf("пустые данные: %q", got)
	}
	if got := FormatData("AutoHDREnable=0;GpuPreference=2;", PowerSaving); got != "AutoHDREnable=0;GpuPreference=1;" {
		t.Errorf("чужие параметры должны сохраняться: %q", got)
	}
}

func TestHybridIgnoresSoftwareAndMicrosoftAdapters(t *testing.T) {
	if Hybrid(oneGPU) {
		t.Error("NVIDIA + Remote Display Adapter — не гибрид")
	}
	if Hybrid([]Adapter{nvidia, basic}) {
		t.Error("NVIDIA + Basic Render Driver — не гибрид")
	}
	if !Hybrid(hybrid) {
		t.Error("Intel + NVIDIA — гибрид")
	}
	if Hybrid(nil) {
		t.Error("пустой список — не гибрид")
	}
}

func TestEnsureDefaultWritesPowerSavingOnlyOnHybridAndOnlyIfUnset(t *testing.T) {
	st := newMem()
	if ch, err := EnsureDefault(st, exePath, oneGPU, nvidia.Name); err != nil || ch || st.setCall != 0 {
		t.Fatalf("одна видеокарта: ничего писать нельзя (changed=%v err=%v)", ch, err)
	}
	ch, err := EnsureDefault(st, exePath, hybrid, intel.Name)
	if err != nil || !ch {
		t.Fatalf("гибрид, значения нет: changed=%v err=%v", ch, err)
	}
	if got := st.m[exePath]; got != "GpuPreference=1;" {
		t.Fatalf("записано %q", got)
	}
	if ch, err := EnsureDefault(st, exePath, hybrid, intel.Name); err != nil || ch || st.setCall != 1 {
		t.Fatalf("повторный вызов не должен писать: changed=%v calls=%d", ch, st.setCall)
	}
}

func TestEnsureDefaultKeepsUserChoiceAndOtherPaths(t *testing.T) {
	st := newMem()
	st.m[strings.ToUpper(exePath)] = "GpuPreference=2;" // выбор человека в «Параметрах Windows» (другой регистр)
	other := `D:\old\AirSpoonSender.exe`
	st.m[other] = "GpuPreference=1;"
	if ch, _ := EnsureDefault(st, exePath, hybrid, intel.Name); ch || st.setCall != 0 {
		t.Fatal("выбор, сделанный человеком, нельзя перезаписывать")
	}
	// Программу перенесли: для нового пути записывается заново, старый не трогаем.
	moved := `E:\new\AirSpoonSender.exe`
	if ch, err := EnsureDefault(st, moved, hybrid, intel.Name); err != nil || !ch {
		t.Fatalf("новый путь: changed=%v err=%v", ch, err)
	}
	if st.m[other] != "GpuPreference=1;" || st.m[moved] != "GpuPreference=1;" || len(st.m) != 3 {
		t.Fatalf("реестр после переноса: %v", st.m)
	}
}

func TestEnsureDefaultPreservesOtherTokensWithoutPreference(t *testing.T) {
	st := newMem()
	st.m[exePath] = "AutoHDREnable=0;" // значение есть, но без GpuPreference
	ch, err := EnsureDefault(st, exePath, hybrid, intel.Name)
	if err != nil || !ch || st.m[exePath] != "AutoHDREnable=0;GpuPreference=1;" {
		t.Fatalf("changed=%v err=%v data=%q", ch, err, st.m[exePath])
	}
}

func TestEnsureDefaultReportsErrors(t *testing.T) {
	st := newMem()
	st.setErr = errors.New("нет доступа")
	if _, err := EnsureDefault(st, exePath, hybrid, intel.Name); err == nil {
		t.Fatal("ошибка записи должна вернуться")
	}
	st = newMem()
	st.getErr = errors.New("реестр недоступен")
	if ch, err := EnsureDefault(st, exePath, hybrid, intel.Name); err == nil || ch {
		t.Fatalf("ошибка чтения: changed=%v err=%v", ch, err)
	}
	if ch, err := EnsureDefault(newMem(), "", hybrid, intel.Name); ch || err != nil {
		t.Fatal("пустой путь игнорируется")
	}
}

func TestChooseFollowsMonitorOwner(t *testing.T) {
	// Монитор на встроенной Intel -> энергосбережение.
	if got := Choose(hybrid, intel.Name); got != PowerSaving {
		t.Errorf("монитор на Intel: %v", got)
	}
	// Монитор на дискретной NVIDIA (внешний монитор у настольного ПК) -> высокая производительность.
	if got := Choose(hybrid, nvidia.Name); got != HighPerformance {
		t.Errorf("монитор на NVIDIA: %v", got)
	}
	if got := Choose(hybrid, "intel(r) uhd graphics"); got != PowerSaving {
		t.Errorf("сравнение имён без учёта регистра: %v", got)
	}
	// Владелец неизвестен -> типичный ноутбук.
	if got := Choose(hybrid, ""); got != PowerSaving {
		t.Errorf("владелец неизвестен: %v", got)
	}
	if got := Choose(hybrid, "Неизвестная карта"); got != PowerSaving {
		t.Errorf("владелец не в списке: %v", got)
	}
	if got := Choose(oneGPU, nvidia.Name); got != PowerSaving {
		t.Errorf("одна карта: %v", got)
	}
}

func TestPlanAfterUnsupported(t *testing.T) {
	// Первый старт: ничего не записано, монитор на Intel -> записать энергосбережение и перезапуститься.
	p := PlanAfterUnsupported(Unspecified, false, hybrid, intel.Name, false)
	if !p.Restart || p.Pref != PowerSaving {
		t.Fatalf("первый план: %+v", p)
	}
	// Энергосбережение уже записано (мы сделали это при старте), но не помогло -> «решает система».
	p = PlanAfterUnsupported(PowerSaving, true, hybrid, intel.Name, false)
	if !p.Restart || p.Pref != Unspecified {
		t.Fatalf("энергосбережение не помогло: %+v", p)
	}
	// Монитор на NVIDIA, а записано энергосбережение -> высокая производительность.
	p = PlanAfterUnsupported(PowerSaving, true, hybrid, nvidia.Name, false)
	if !p.Restart || p.Pref != HighPerformance {
		t.Fatalf("монитор на NVIDIA: %+v", p)
	}
	// Человек выбрал высокую производительность, монитор на Intel -> энергосбережение.
	p = PlanAfterUnsupported(HighPerformance, true, hybrid, intel.Name, false)
	if !p.Restart || p.Pref != PowerSaving {
		t.Fatalf("выбор человека: %+v", p)
	}
}

func TestPlanAfterUnsupportedNeverLoops(t *testing.T) {
	// Защита от цикла: после перезапуска повторно не перезапускаемся.
	if p := PlanAfterUnsupported(Unspecified, false, hybrid, intel.Name, true); p.Restart || p.Reason == "" {
		t.Fatalf("уже перезапускались: %+v", p)
	}
	// Одна видеокарта: смена предпочтения не поможет.
	if p := PlanAfterUnsupported(Unspecified, false, oneGPU, "", false); p.Restart {
		t.Fatalf("одна видеокарта: %+v", p)
	}
	// Сам по себе перебор предпочтений бесконечен (1 -> 0 -> 1 ...): конец ему
	// кладёт только флаг alreadyRetried, который вызывающий берёт из окружения
	// процесса, запущенного после перезапуска. Проверяем, что флаг работает при
	// любом исходном состоянии.
	for _, cur := range []Preference{Unspecified, PowerSaving, HighPerformance} {
		for _, set := range []bool{false, true} {
			if p := PlanAfterUnsupported(cur, set, hybrid, intel.Name, true); p.Restart {
				t.Fatalf("cur=%v set=%v: перезапуск после перезапуска", cur, set)
			}
		}
	}
}

// В сеансе удалённого рабочего стола одна и та же NVIDIA может быть перечислена
// дважды (разные LUID): это не гибридная система.
func TestHybridIgnoresDuplicatedAdapter(t *testing.T) {
	a := Adapter{Name: "NVIDIA GeForce RTX 2080 Ti", VendorID: 0x10DE, DeviceID: 0x1E07, VideoMemory: 11 << 30}
	b := a
	if Hybrid([]Adapter{a, b, basic}) {
		t.Fatal("две записи одной NVIDIA — не гибрид")
	}
	if got := len(Distinct([]Adapter{a, b})); got != 1 {
		t.Fatalf("Distinct: %d", got)
	}
	// Одинаковые поставщик и название, но другая модель — другая видеокарта.
	c := a
	c.DeviceID = 0x2204
	if !Hybrid([]Adapter{a, c}) {
		t.Fatal("разные модели — две видеокарты")
	}
	if p := PlanAfterUnsupported(Unspecified, false, []Adapter{a, b}, a.Name, false); p.Restart {
		t.Fatalf("одна видеокарта (дубль в списке): перезапуск бесполезен: %+v", p)
	}
}

// Настольный ПК с Intel и NVIDIA: монитор на NVIDIA. Уводить процесс на встроенную
// карту нельзя; владелец неизвестен — тоже ничего не пишем.
func TestEnsureDefaultWritesOnlyWhenMonitorIsOnLowPowerAdapter(t *testing.T) {
	for name, owner := range map[string]string{
		"монитор на дискретной карте": nvidia.Name,
		"владелец неизвестен":         "",
		"владелец не из списка":       "Неизвестная карта",
	} {
		st := newMem()
		if ch, err := EnsureDefault(st, exePath, hybrid, owner); err != nil || ch || st.setCall != 0 || len(st.m) != 0 {
			t.Errorf("%s: писать нельзя (changed=%v err=%v реестр=%v)", name, ch, err, st.m)
		}
	}
	st := newMem()
	if ch, err := EnsureDefault(st, exePath, hybrid, "intel(r) uhd graphics"); err != nil || !ch || st.m[exePath] != "GpuPreference=1;" {
		t.Fatalf("монитор на встроенной карте: changed=%v err=%v реестр=%v", ch, err, st.m)
	}
}

func TestOwnedByLowPower(t *testing.T) {
	if !OwnedByLowPower(hybrid, intel.Name) {
		t.Error("монитор на Intel при Intel+NVIDIA: да")
	}
	if OwnedByLowPower(hybrid, nvidia.Name) {
		t.Error("монитор на NVIDIA: нет")
	}
	if OwnedByLowPower(oneGPU, nvidia.Name) || OwnedByLowPower(nil, "") {
		t.Error("одна видеокарта: нет")
	}
}

// Память о прежних неудачах делает перебор конечным и между запусками: 0 -> 1
// (исходное «решает система» уже не подошло), дальше только совет (раньше реестр
// гулял 1 -> 0 -> 1 -> ...).
func TestPlanAfterUnsupportedTriedIsFinite(t *testing.T) {
	var tried []Preference
	cur, set := Unspecified, false
	var steps []Preference
	for i := 0; i < 10; i++ {
		p := PlanAfterUnsupportedTried(cur, set, hybrid, intel.Name, false, tried)
		tried = p.Failed // вызывающий сохраняет Failed между запусками
		if !p.Restart {
			break
		}
		steps = append(steps, p.Pref)
		cur, set = p.Pref, true
	}
	if len(steps) != 1 || steps[0] != PowerSaving {
		t.Fatalf("ожидалась одна смена на энергосбережение и остановка, получено %v", steps)
	}
	// Выбор человека из списка не входит: он ещё не пробовался.
	p := PlanAfterUnsupportedTried(HighPerformance, true, hybrid, intel.Name, false, tried)
	if p.Restart {
		t.Fatalf("все допустимые предпочтения уже перебраны: %+v", p)
	}
}

// Владелец монитора не опознан как настоящая видеокарта (пусто, неизвестное имя,
// «Microsoft Remote Display Adapter»): предпочтение не меняется и перезапуска нет.
func TestPlanAfterUnsupportedRefusesUnknownOwner(t *testing.T) {
	for _, owner := range []string{"", "Неизвестная карта", "Microsoft Remote Display Adapter"} {
		if OwnerKnown(hybrid, owner) {
			t.Errorf("OwnerKnown(%q) = true", owner)
		}
		p := PlanAfterUnsupported(Unspecified, false, hybrid, owner, false)
		if p.Restart || p.Reason == "" {
			t.Errorf("владелец %q: ожидался отказ с пояснением, получено %+v", owner, p)
		}
	}
	if !OwnerKnown(hybrid, "intel(r) uhd graphics") {
		t.Error("известный владелец (без учёта регистра) не опознан")
	}
}
