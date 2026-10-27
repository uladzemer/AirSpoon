//go:build !windows

package airplay

// adapterDescriptions: описаний драйверов вне Windows нет, работает фильтр по имени.
func adapterDescriptions() map[int]string { return nil }
