//go:build windows

package gpudiag

import (
	"fmt"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// SystemInfo — сведения о компьютере для разбора «на этой машине не работает»:
// версия и редакция Windows, архитектура, процессор, память, питание.
type SystemInfo struct {
	Windows      string // "Windows 11 Pro 24H2, сборка 10.0.26100.1742"
	Edition      string // EditionID: Professional, CoreN...
	IsN          bool   // редакция N/KN (без Media Foundation, пока не поставлен пакет)
	ProcessArch  string // архитектура программы
	SystemArch   string // архитектура Windows
	CPU          string
	LogicalCores int
	RAMTotalMB   uint64
	RAMFreeMB    uint64
	Machine      string // производитель и модель по BIOS (отличает виртуальные машины)
	Power        string // "от сети", "от батареи (56%)"
}

// isNEdition определяет редакции N и KN Windows (без мультимедийных функций) по
// EditionID ("ProfessionalN", "CoreN") или названию ("Windows 10 Pro N", "... KN").
func isNEdition(editionID, productName string) bool {
	if strings.HasSuffix(editionID, "N") && editionID != "N" {
		return true
	}
	for _, suffix := range []string{" N", " KN"} {
		if strings.HasSuffix(productName, suffix) || strings.Contains(productName, suffix+" ") {
			return true
		}
	}
	return false
}

// machineName склеивает производителя и модель, не повторяя общих слов.
func machineName(manufacturer, product string) string {
	manufacturer, product = strings.TrimSpace(manufacturer), strings.TrimSpace(product)
	switch {
	case manufacturer == "" && product == "":
		return ""
	case manufacturer == "":
		return product
	case product == "" || strings.EqualFold(manufacturer, product):
		return manufacturer
	}
	return manufacturer + " " + product
}

func regString(root registry.Key, path, name string) string {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	s, _, err := k.GetStringValue(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func regDWord(root registry.Key, path, name string) (uint64, bool) {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return 0, false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(name)
	return v, err == nil
}

func archName(machine uint16) string {
	switch machine {
	case 0x8664:
		return "x64 (amd64)"
	case 0xAA64:
		return "ARM64"
	case 0x14c:
		return "x86 (32 бита)"
	case 0x1c4:
		return "ARM (32 бита)"
	case 0:
		return "неизвестна"
	}
	return fmt.Sprintf("0x%X", machine)
}

func goArchName(a string) string {
	switch a {
	case "amd64":
		return "x64 (amd64)"
	case "arm64":
		return "ARM64"
	case "386":
		return "x86 (32 бита)"
	}
	return a
}

type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

var procGetSystemPowerStatus = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemPowerStatus")

// powerText описывает питание по GetSystemPowerStatus.
func powerText(s systemPowerStatus) string {
	if s.BatteryFlag == 128 {
		return "от сети (батареи нет)"
	}
	pct := ""
	if s.BatteryLifePercent <= 100 {
		pct = fmt.Sprintf(", заряд %d%%", s.BatteryLifePercent)
	}
	switch s.ACLineStatus {
	case 1:
		return "от сети" + pct
	case 0:
		return "от батареи" + pct
	}
	return "неизвестно" + pct
}

func readPower() string {
	var s systemPowerStatus
	if r, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&s))); r == 0 {
		return "не определено"
	}
	return powerText(s)
}

// collectSystem читает сведения о системе; недоступное поле остаётся пустым.
func collectSystem() SystemInfo {
	const cv = `SOFTWARE\Microsoft\Windows NT\CurrentVersion`
	v := windows.RtlGetVersion()
	si := SystemInfo{LogicalCores: runtime.NumCPU(), ProcessArch: goArchName(runtime.GOARCH)}

	product := regString(registry.LOCAL_MACHINE, cv, "ProductName")
	// ProductName на Windows 11 по-прежнему начинается с «Windows 10».
	if v.BuildNumber >= 22000 {
		product = strings.Replace(product, "Windows 10", "Windows 11", 1)
	}
	if product == "" {
		product = "Windows"
	}
	si.Edition = regString(registry.LOCAL_MACHINE, cv, "EditionID")
	si.IsN = isNEdition(si.Edition, product)
	ver := regString(registry.LOCAL_MACHINE, cv, "DisplayVersion")
	if ver == "" {
		ver = regString(registry.LOCAL_MACHINE, cv, "ReleaseId")
	}
	build := fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
	if ubr, ok := regDWord(registry.LOCAL_MACHINE, cv, "UBR"); ok {
		build += fmt.Sprintf(".%d", ubr)
	}
	si.Windows = product
	if ver != "" {
		si.Windows += " " + ver
	}
	si.Windows += ", сборка " + build

	var pm, nm uint16
	if err := windows.IsWow64Process2(windows.CurrentProcess(), &pm, &nm); err == nil {
		si.SystemArch = archName(nm)
	} else {
		si.SystemArch = goArchName(runtime.GOARCH) // IsWow64Process2 нет до Windows 10 1511
	}

	si.CPU = regString(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, "ProcessorNameString")
	si.Machine = machineName(
		regString(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\BIOS`, "SystemManufacturer"),
		regString(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\BIOS`, "SystemProductName"))

	var ms memoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms))); r != 0 {
		si.RAMTotalMB, si.RAMFreeMB = ms.TotalPhys>>20, ms.AvailPhys>>20
	}
	si.Power = readPower()
	return si
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys, AvailPhys uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

var procGlobalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// Lines — сведения о системе построчно.
func (s SystemInfo) Lines() []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	nEd := "нет"
	if s.IsN {
		nEd = "ДА (N/KN: Media Foundation есть только после установки «Пакета мультимедийных функций»)"
	}
	add("Система: %s; редакция %s, N/KN: %s", s.Windows, orDash(s.Edition), nEd)
	add("  Архитектура: программа %s, Windows %s", s.ProcessArch, s.SystemArch)
	cpu := s.CPU
	if cpu == "" {
		cpu = "не определён"
	}
	add("  Процессор: %s, логических ядер: %d", cpu, s.LogicalCores)
	if s.RAMTotalMB > 0 {
		add("  Память: всего %d МБ, свободно %d МБ", s.RAMTotalMB, s.RAMFreeMB)
	}
	if s.Machine != "" {
		add("  Компьютер: %s", s.Machine)
	}
	add("  Питание: %s", s.Power)
	return out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
