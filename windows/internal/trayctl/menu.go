package trayctl

import (
	"context"
	"strings"
	"time"
)

// Тексты меню (все по-русски, короткие и понятные).
const (
	TextHeader       = "Показать экран на телевизоре:"
	TextNoDevices    = "Телевизоры не найдены. Проверьте, что телевизор включён и в той же сети Wi‑Fi"
	TextStop         = "■ Остановить показ"
	TextAddress      = "Подключить по адресу…"
	TextAudio        = "Звук с компьютера"
	TextCheckUpdates = "Проверять обновления"
	TextUpdatePrefix = "Обновить до версии "
	TextReport       = "Сохранить отчёт для поддержки"
	TextExit         = "Выход"
)

// ItemKind — вид пункта меню.
type ItemKind int

const (
	ItemHeader ItemKind = iota
	ItemDevice
	ItemNone // «телевизоры не найдены»
	ItemStop
	ItemUpdate // «Обновить до версии X.Y.Z»
	ItemSeparator
	ItemAddress
	ItemAudio
	ItemCheckUpdates // «Проверять обновления»
	ItemReport       // «Сохранить отчёт для поддержки»
	ItemExit
)

// MenuItem — пункт меню для Win32-обёртки. Action вызывается при выборе
// пункта (из любой горутины, не блокирует интерфейс дольше нескольких мс).
type MenuItem struct {
	Kind     ItemKind
	Text     string
	Checked  bool
	Disabled bool
	Action   func()
}

// Menu строит меню по текущему состоянию.
func (c *Controller) Menu() []MenuItem {
	s := c.Snapshot()
	items := []MenuItem{{Kind: ItemHeader, Text: TextHeader, Disabled: true}}

	devices := s.Devices
	// Телевизор, подключённый по адресу, есть в меню, даже если mDNS его не видит.
	// Если mDNS нашёл тот же телевизор (совпал идентификатор или адрес), строка
	// остаётся одна — с именем телевизора, а не второй голый адрес.
	if s.Current != nil {
		found := false
		for _, d := range devices {
			if sameDevice(d, *s.Current) {
				found = true
				break
			}
		}
		if !found {
			devices = append(append([]Device(nil), devices...), *s.Current)
		}
	}
	if len(devices) == 0 {
		// Не серая строка (серый текст пожилым людям читать трудно): выбор строки
		// просто ищет телевизоры заново.
		items = append(items, MenuItem{Kind: ItemNone, Text: TextNoDevices, Action: c.RefreshDevices})
	}
	for _, d := range devices {
		d := d
		items = append(items, MenuItem{
			Kind:    ItemDevice,
			Text:    d.Name,
			Checked: s.Current != nil && sameDevice(d, *s.Current),
			Action:  func() { c.Select(d) },
		})
	}
	if s.Phase != Idle {
		items = append(items, MenuItem{Kind: ItemStop, Text: TextStop, Action: c.Stop})
	}
	if s.AvailableUpdate != "" {
		items = append(items, MenuItem{
			Kind:   ItemUpdate,
			Text:   TextUpdatePrefix + s.AvailableUpdate,
			Action: c.applyUpdate,
		})
	}
	items = append(items,
		MenuItem{Kind: ItemSeparator},
		MenuItem{Kind: ItemAddress, Text: TextAddress, Action: c.promptAddress},
		MenuItem{Kind: ItemAudio, Text: TextAudio, Checked: s.Audio, Action: func() { c.SetAudio(!s.Audio) }},
	)
	if c.updater != nil { // в сборках "dev" проверки нет — и галочки тоже
		items = append(items, MenuItem{Kind: ItemCheckUpdates, Text: TextCheckUpdates, Checked: s.CheckUpdates,
			Action: func() { c.SetCheckUpdates(!s.CheckUpdates) }})
	}
	if c.cfg.SupportReport != nil {
		items = append(items, MenuItem{Kind: ItemReport, Text: TextReport, Action: c.cfg.SupportReport})
	}
	items = append(items,
		MenuItem{Kind: ItemSeparator},
		MenuItem{Kind: ItemExit, Text: TextExit, Action: c.Exit},
	)
	return items
}

// sameDevice — это один и тот же телевизор: совпал идентификатор или адрес
// (подключение «по адресу» даёт ID вида addr:ip:port, а mDNS — идентификатор
// устройства, адрес у них один).
func sameDevice(a, b Device) bool {
	if a.ID != "" && a.ID == b.ID {
		return true
	}
	return a.Addr != "" && strings.EqualFold(a.Addr, b.Addr)
}

// promptAddress спрашивает адрес у пользователя и начинает показ.
func (c *Controller) promptAddress() {
	c.mu.Lock()
	initial, base := c.lastAddr, c.base
	c.mu.Unlock()
	ctx, cancel := context.WithCancel(base)
	defer cancel()
	addr, ok := c.cfg.UI.AskAddress(ctx, initial)
	if !ok {
		return
	}
	if err := c.ConnectAddress(addr); err != nil {
		c.cfg.UI.Notify(NoticeError, AppName,
			"Адрес указан неверно. Он выглядит так: 192.168.1.50 (иногда с номером порта: 192.168.1.50:7000).")
	}
}

// Exit останавливает показ (TEARDOWN) и завершает программу.
func (c *Controller) Exit() {
	if !c.Shutdown(5 * time.Second) {
		c.cfg.Logf("показ не успел корректно завершиться за 5 с")
	}
	c.cfg.UI.Quit()
}
