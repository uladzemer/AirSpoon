// Пакет gpupref — выбор видеокарты, на которой работает сам процесс, для
// ноутбуков с двумя видеокартами (встроенная Intel/AMD + дискретная NVIDIA/AMD).
//
// Известное ограничение Windows: Desktop Duplication (захват экрана) не работает,
// если процесс выполняется на дискретной видеокарте, а экран подключён к
// встроенной: IDXGIOutput1::DuplicateOutput возвращает DXGI_ERROR_UNSUPPORTED
// (0x887A0004). Microsoft: https://learn.microsoft.com/en-us/troubleshoot/windows-client/shell-experience/error-when-dda-capable-app-is-against-gpu
// (KB 3019314) — «запускайте приложение на встроенной видеокарте».
//
// Надёжный способ с Windows 10 (сборка 17093) — предпочтение видеокарты для
// конкретной программы, то же, что «Параметры > Система > Дисплей > Графика»:
// значение HKCU\Software\Microsoft\DirectX\UserGpuPreferences, имя — полный путь
// к .exe, данные — строка «GpuPreference=N;» (0 — решает система, 1 —
// энергосбережение, обычно встроенная, 2 — высокая производительность). Видеокарту
// определяет процесс при старте, поэтому изменение действует на следующий запуск
// процесса (у драйвера NVIDIA тоже: «устройство отрисовки определяется при старте
// приложения»).
//
// Логика здесь не зависит от Windows: реестр и перечень видеокарт приходят через
// интерфейс Store и структуры Adapter, поэтому всё проверяется тестами с подделками.
package gpupref

import (
	"fmt"
	"strconv"
	"strings"
)

// Preference — значение GpuPreference.
type Preference int

const (
	// Unspecified — решает Windows (GpuPreference=0).
	Unspecified Preference = 0
	// PowerSaving — энергосбережение, на ноутбуках встроенная видеокарта (1).
	PowerSaving Preference = 1
	// HighPerformance — высокая производительность, дискретная видеокарта (2).
	HighPerformance Preference = 2
)

func (p Preference) String() string {
	switch p {
	case Unspecified:
		return "решает система"
	case PowerSaving:
		return "энергосбережение"
	case HighPerformance:
		return "высокая производительность"
	}
	return "значение " + strconv.Itoa(int(p))
}

// Store — хранилище предпочтений по пути к .exe (в Windows — реестр).
type Store interface {
	// Get возвращает строку-данные значения для программы exe; ok=false — не задано.
	Get(exe string) (data string, ok bool, err error)
	// Set записывает строку-данные значения.
	Set(exe, data string) error
}

// Adapter — видеокарта в том виде, который нужен для решений.
type Adapter struct {
	Name        string
	VendorID    uint32
	DeviceID    uint32
	Software    bool   // программный (Microsoft Basic Render Driver и подобные)
	VideoMemory uint64 // выделенная видеопамять, байт
	Outputs     int    // сколько выходов (мониторов) видит процесс на этой карте
}

const vendorMicrosoft = 0x1414

// Real сообщает, что это настоящая видеокарта (не программный и не
// виртуальный адаптер Microsoft вроде Basic Render Driver и Remote Display).
func (a Adapter) Real() bool { return !a.Software && a.VendorID != vendorMicrosoft }

// Real возвращает только настоящие видеокарты.
func Real(list []Adapter) []Adapter {
	var out []Adapter
	for _, a := range list {
		if a.Real() {
			out = append(out, a)
		}
	}
	return out
}

// Distinct убирает повторы одной и той же видеокарты (одинаковые поставщик,
// модель и название): в сеансе удалённого рабочего стола одна и та же NVIDIA
// может быть перечислена дважды под разными LUID, это не вторая видеокарта.
func Distinct(list []Adapter) []Adapter {
	type key struct {
		vendor, device uint32
		name           string
	}
	seen := map[key]bool{}
	var out []Adapter
	for _, a := range list {
		k := key{a.VendorID, a.DeviceID, strings.ToLower(a.Name)}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, a)
	}
	return out
}

// Hybrid сообщает, что в системе больше одной разной настоящей видеокарты.
func Hybrid(list []Adapter) bool { return len(Distinct(Real(list))) > 1 }

// ParseData разбирает данные значения («GpuPreference=1;» и, например,
// «AutoHDREnable=0;GpuPreference=1;») и возвращает предпочтение.
func ParseData(data string) (Preference, bool) {
	for _, tok := range strings.Split(data, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(tok), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "GpuPreference") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 0 || n > 2 {
			return 0, false
		}
		return Preference(n), true
	}
	return 0, false
}

// FormatData возвращает данные значения с заданным предпочтением, сохраняя
// чужие параметры из прежних данных (например, AutoHDREnable).
func FormatData(old string, p Preference) string {
	var kept []string
	for _, tok := range strings.Split(old, ";") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if k, _, ok := strings.Cut(tok, "="); ok && strings.EqualFold(strings.TrimSpace(k), "GpuPreference") {
			continue
		}
		kept = append(kept, tok)
	}
	kept = append(kept, fmt.Sprintf("GpuPreference=%d", int(p)))
	return strings.Join(kept, ";") + ";"
}

// Current возвращает предпочтение, заданное для программы; set=false — не задано.
func Current(st Store, exe string) (p Preference, set bool, err error) {
	data, ok, err := st.Get(exe)
	if err != nil || !ok {
		return Unspecified, false, err
	}
	p, ok = ParseData(data)
	return p, ok, nil
}

// OwnedByLowPower сообщает, что в системе больше одной разной настоящей
// видеокарты и монитор (по данным GDI, owner — название видеокарты-владельца)
// подключён к встроенной. Только тогда «энергосбережение» заведомо верно:
// процесс окажется на той же карте, что и экран. Владелец неизвестен или это
// дискретная карта (настольный ПК: монитор в карте, встроенная графика процессора
// простаивает) — false.
func OwnedByLowPower(adapters []Adapter, owner string) bool {
	real := Distinct(Real(adapters))
	if len(real) < 2 || owner == "" {
		return false
	}
	for i, a := range real {
		if strings.EqualFold(a.Name, owner) {
			return i == lowPower(real)
		}
	}
	return false
}

// EnsureDefault при старте программы записывает «энергосбережение» для её
// пути, если видеокарт больше одной, захватываемый монитор (по данным GDI, owner)
// подключён к встроенной видеокарте и предпочтение для этого пути ещё не задано
// (ни нами, ни человеком в настройках Windows). Если монитор на дискретной карте
// (настольный ПК с картой в слоте и встроенной графикой процессора) или владельца
// определить не удалось, ничего не пишется: уход процесса на встроенную карту
// там сломал бы захват, а не починил; если окажется, что захват не открылся,
// предпочтение подберёт PlanAfterUnsupported. Путь — полный путь к .exe: при
// переносе или обновлении файла по новому пути значение записывается заново,
// а записи старого пути остаются нетронутыми. changed=true — запись сделана,
// и программе нужно стартовать заново, чтобы предпочтение подействовало.
func EnsureDefault(st Store, exe string, adapters []Adapter, owner string) (changed bool, err error) {
	if exe == "" || !OwnedByLowPower(adapters, owner) {
		return false, nil
	}
	_, set, err := Current(st, exe)
	if err != nil || set {
		return false, err
	}
	old, _, _ := st.Get(exe)
	if err := st.Set(exe, FormatData(old, PowerSaving)); err != nil {
		return false, err
	}
	return true, nil
}

// lowPower возвращает встроенную (экономичную) видеокарту: с наименьшей
// выделенной видеопамятью, при равенстве — Intel (0x8086). Настоящая встроенная
// видеокарта делит память с процессором, то есть выделенной почти нет.
func lowPower(real []Adapter) int {
	best := 0
	for i, a := range real {
		b := real[best]
		switch {
		case a.VideoMemory < b.VideoMemory:
			best = i
		case a.VideoMemory == b.VideoMemory && a.VendorID == 0x8086 && b.VendorID != 0x8086:
			best = i
		}
	}
	return best
}

// OwnerKnown сообщает, что owner — название одной из настоящих видеокарт
// системы. Виртуальный адаптер (например, «Microsoft Remote Display Adapter» в
// сеансе удалённого рабочего стола) или пустое имя — нет: угадывать по ним
// видеокарту нельзя.
func OwnerKnown(adapters []Adapter, owner string) bool {
	if owner == "" {
		return false
	}
	for _, a := range Distinct(Real(adapters)) {
		if strings.EqualFold(a.Name, owner) {
			return true
		}
	}
	return false
}

// Choose выбирает предпочтение, при котором процесс окажется на той же
// видеокарте, к которой подключён захватываемый монитор. owner — название
// видеокарты, которой монитор принадлежит по данным GDI (EnumDisplayDevices;
// DXGI в процессе на «чужой» карте показывает выход под ней). Если владельца
// найти не удалось — предполагаем типичный ноутбук: экран на встроенной карте.
func Choose(adapters []Adapter, owner string) Preference {
	real := Distinct(Real(adapters))
	if len(real) < 2 || owner == "" {
		return PowerSaving
	}
	for i, a := range real {
		if strings.EqualFold(a.Name, owner) {
			if i == lowPower(real) {
				return PowerSaving
			}
			return HighPerformance
		}
	}
	return PowerSaving
}

// Plan — решение, что делать, когда DuplicateOutput вернул DXGI_ERROR_UNSUPPORTED.
type Plan struct {
	// Restart — записать Pref и один раз перезапустить программу.
	Restart bool
	Pref    Preference
	// Failed — предпочтения, при которых захват уже не открылся (прежние
	// неудачные и действующее сейчас); вызывающий сохраняет их между запусками,
	// чтобы не гонять реестр по кругу (1 -> 0 -> 1 ...).
	Failed []Preference
	// Reason — пояснение для журнала.
	Reason string
}

// PlanAfterUnsupported — PlanAfterUnsupportedTried без памяти о прежних неудачах.
func PlanAfterUnsupported(cur Preference, curSet bool, adapters []Adapter, owner string, alreadyRetried bool) Plan {
	return PlanAfterUnsupportedTried(cur, curSet, adapters, owner, alreadyRetried, nil)
}

// PlanAfterUnsupportedTried решает, стоит ли исправлять предпочтение видеокарты
// и перезапускаться. В пределах одного запуска перезапуск делается не больше
// одного раза: alreadyRetried (по переменной окружения процесса, который запущен
// после такого перезапуска) его запрещает. Порядок предпочтений: сначала то, что
// подходит владельцу монитора (Choose), затем «решает система» (так делает и
// Sunshine: без выбранной карты Windows не переносит выходы на карту отрисовки);
// берётся первое, которое отличается от действующего сейчас и не значится в tried
// (предпочтения, при которых захват уже не открылся в прошлых запусках). Так
// перебор конечен и между запусками: когда подходящих не осталось, совет — настроить
// вручную и запустить diag.
func PlanAfterUnsupportedTried(cur Preference, curSet bool, adapters []Adapter, owner string, alreadyRetried bool, tried []Preference) Plan {
	if alreadyRetried {
		return Plan{Reason: "перезапуск для смены видеокарты уже выполнялся"}
	}
	if !Hybrid(adapters) {
		return Plan{Reason: "в системе одна видеокарта: смена предпочтения не поможет"}
	}
	if !OwnerKnown(adapters, owner) {
		return Plan{Reason: fmt.Sprintf("владелец монитора не опознан как настоящая видеокарта (%q): предпочтение не меняется", owner)}
	}
	effective := Unspecified
	if curSet {
		effective = cur
	}
	failed := appendPref(append([]Preference(nil), tried...), effective)
	want := Choose(adapters, owner)
	for _, p := range []Preference{want, Unspecified} {
		if !hasPref(failed, p) {
			return Plan{Restart: true, Pref: p, Failed: failed, Reason: fmt.Sprintf("предпочтение видеокарты: %s -> %s", effective, p)}
		}
	}
	return Plan{Failed: failed, Reason: "все предпочтения видеокарты уже перебраны"}
}

func hasPref(list []Preference, p Preference) bool {
	for _, x := range list {
		if x == p {
			return true
		}
	}
	return false
}

func appendPref(list []Preference, p Preference) []Preference {
	if hasPref(list, p) {
		return list
	}
	return append(list, p)
}
