//go:build windows

package traywin

import (
	"golang.org/x/sys/windows/registry"
)

// Тема панели задач. Значок трея одноцветный: белый на тёмной панели и чёрный
// на светлой. Тему панели задач и области уведомлений задаёт значение
// SystemUsesLightTheme (а не AppsUseLightTheme, которое относится к окнам
// приложений). Windows сообщает о смене темы сообщением WM_SETTINGCHANGE.
const personalizeKey = `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`

const wmSettingChange = 0x001A

// Имена ресурсов значков в .exe (см. winres.json).
const (
	iconAppName       = "APP"   // цветной значок программы (окна, ярлыки)
	iconTrayWhiteName = "TRAYW" // белый знак для тёмной панели
	iconTrayBlackName = "TRAYK" // чёрный знак для светлой панели
	iconLiveWhiteName = "LIVEW" // то же, малый экран цветом #4F8CFF
	iconLiveBlackName = "LIVEK"
)

// trayIconName выбирает ресурс значка трея по теме панели и состоянию показа.
func trayIconName(lightTheme, live bool) string {
	switch {
	case lightTheme && live:
		return iconLiveBlackName
	case lightTheme:
		return iconTrayBlackName
	case live:
		return iconLiveWhiteName
	}
	return iconTrayWhiteName
}

// themeReader читает DWORD-значение темы; подменяется в тестах.
type themeReader func(name string) (uint32, bool)

func registryTheme(name string) (uint32, bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, personalizeKey, registry.QUERY_VALUE)
	if err != nil {
		return 0, false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(name)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

// panelUsesLightTheme сообщает, светлая ли панель задач. Нет значения (старые
// сборки Windows 10) — считаем панель тёмной, как в Windows по умолчанию.
func panelUsesLightTheme(read themeReader) bool {
	v, ok := read("SystemUsesLightTheme")
	return ok && v != 0
}
