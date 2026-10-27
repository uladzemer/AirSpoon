package airplay

import (
	"net"
	"strings"
)

// Общие помощники слоёв поиска: из имени, адресов, порта и TXT-пар собирается
// AirPlayDevice одинаково для любого слоя (Windows, unicast, zeroconf).

// deviceFromService собирает приёмник; nil, если нет ни одного пригодного адреса.
// label — уже готовое имя экземпляра («Телевизор»), txt — ключи в нижнем регистре.
func deviceFromService(label string, ips []net.IP, port int, txt map[string]string) *AirPlayDevice {
	ip := pickServiceIP(ips)
	if ip == nil || port <= 0 || port > 65535 {
		return nil
	}
	lower := make(map[string]string, len(txt))
	for k, v := range txt {
		lower[strings.ToLower(k)] = v
	}
	dev := &AirPlayDevice{Name: label, IP: ip.String(), Port: port}
	populateDeviceFromTXT(dev, lower)
	return dev
}

// pickServiceIP предпочитает IPv4; IPv6 link-local без зоны не годится для подключения.
func pickServiceIP(ips []net.IP) net.IP {
	usable := usableServiceIPs(ips)
	for _, ip := range usable {
		if ip.To4() != nil {
			return ip
		}
	}
	if len(usable) > 0 {
		return usable[0]
	}
	return nil
}

func usableServiceIPs(ips []net.IP) []net.IP {
	var out []net.IP
	for _, ip := range ips {
		if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() {
			continue
		}
		if ip.To4() == nil && ip.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, ip)
	}
	return out
}

// instanceLabel убирает с полного имени экземпляра «<имя>._airplay._tcp.local[.]»
// суффикс сервиса; точки внутри имени сохраняются.
func instanceLabel(full, serviceType string) string {
	full = strings.TrimSuffix(full, ".")
	suffix := "." + serviceType + ".local"
	if len(full) >= len(suffix) && strings.EqualFold(full[len(full)-len(suffix):], suffix) {
		return full[:len(full)-len(suffix)]
	}
	return full
}
