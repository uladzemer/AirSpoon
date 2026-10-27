package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"airspoon/internal/airplay"
	"airspoon/internal/applog"
)

// defaultLastReceiverFile — файл последнего успешного телевизора рядом с журналом.
func defaultLastReceiverFile() string {
	return filepath.Join(filepath.Dir(applog.DefaultPath()), "last-receiver.json")
}

func runList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	timeout := fs.Duration("timeout", 3*time.Second, "сколько искать приёмники")
	verbose := fs.Bool("verbose", false, "подробный лог")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	setupLogging(*verbose)

	devices, err := discover(context.Background(), *timeout)
	if err != nil {
		log.Printf("ошибка поиска приёмников: %v", err)
		return 1
	}
	if len(devices) == 0 {
		fmt.Println("приёмники AirPlay не найдены")
		return 0
	}
	printDevices(os.Stdout, devices)
	return 0
}

// discover ищет приёмники в течение timeout и возвращает их отсортированными.
func discover(ctx context.Context, timeout time.Duration) ([]airplay.AirPlayDevice, error) {
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rep := airplay.DiscoverAirPlay(dctx, airplay.DiscoverOptions{LastReceiverPath: defaultLastReceiverFile()})
	// Разовый поиск (list, mirror --name): итог по слоям пишем всегда.
	log.Printf("поиск: %s", rep.Summary())
	if err := rep.Err(); err != nil {
		return nil, err
	}
	devices := rep.Devices
	sort.SliceStable(devices, func(i, j int) bool {
		if devices[i].IP != devices[j].IP {
			return devices[i].IP < devices[j].IP
		}
		return strings.ToLower(devices[i].Name) < strings.ToLower(devices[j].Name)
	})
	return devices, nil
}

func printDevices(w io.Writer, devices []airplay.AirPlayDevice) {
	for i, d := range devices {
		mirroring := "нет"
		if d.SupportsScreen() {
			mirroring = "да"
		}
		fmt.Fprintf(w, "[%d] имя=%s адрес=%s порт=%d модель=%s зеркалирование=%s features=0x%x",
			i+1, strconv.Quote(d.Name), d.IP, d.Port, strconv.Quote(d.Model), mirroring, d.Features)
		if len(d.FeaturesEx) > 0 {
			fmt.Fprintf(w, " featuresEx=%x", []byte(d.FeaturesEx))
		}
		if d.SourceVersion != "" {
			fmt.Fprintf(w, " srcvers=%s", d.SourceVersion)
		}
		if d.DeviceID != "" {
			fmt.Fprintf(w, " id=%s", d.DeviceID)
		}
		if len(d.FoundBy) > 0 {
			fmt.Fprintf(w, " найден=%s", strings.Join(d.FoundBy, "+"))
		}
		fmt.Fprintln(w)
	}
}

// splitHostPort разбирает "адрес[:порт]"; порт по умолчанию 7000.
func splitHostPort(value string, defaultPort int) (string, int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", 0, fmt.Errorf("пустой адрес")
	}
	host, portStr, err := net.SplitHostPort(value)
	if err != nil {
		// Нет порта (или это голый IPv6).
		return strings.Trim(value, "[]"), defaultPort, nil
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("недопустимый порт %q", portStr)
	}
	return host, port, nil
}

// findByName выбирает приёмник по точному имени, затем без учёта регистра.
func findByName(devices []airplay.AirPlayDevice, name string) (*airplay.AirPlayDevice, error) {
	var matches []int
	for i := range devices {
		if devices[i].Name == name {
			matches = append(matches, i)
		}
	}
	if len(matches) == 0 {
		for i := range devices {
			if strings.EqualFold(devices[i].Name, name) {
				matches = append(matches, i)
			}
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("приёмник %q не найден (используйте \"airspoon list\" или --host)", name)
	case 1:
		d := devices[matches[0]]
		return &d, nil
	default:
		return nil, fmt.Errorf("имя %q неоднозначно: найдено %d приёмников, укажите --host", name, len(matches))
	}
}
