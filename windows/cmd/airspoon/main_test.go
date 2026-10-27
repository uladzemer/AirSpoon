package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"airspoon/internal/airplay"
)

func TestSplitHostPort(t *testing.T) {
	for _, c := range []struct {
		in   string
		host string
		port int
		bad  bool
	}{
		{"100.65.152.57:7000", "100.65.152.57", 7000, false},
		{"100.65.152.57", "100.65.152.57", 7000, false},
		{"127.0.0.1:7100", "127.0.0.1", 7100, false},
		{"[::1]:7001", "::1", 7001, false},
		{"fe80::1", "fe80::1", 7000, false},
		{"tv.local", "tv.local", 7000, false},
		{"1.2.3.4:99999", "", 0, true},
		{"1.2.3.4:abc", "", 0, true},
		{"", "", 0, true},
	} {
		host, port, err := splitHostPort(c.in, 7000)
		if c.bad {
			if err == nil {
				t.Errorf("%q: ожидалась ошибка", c.in)
			}
			continue
		}
		if err != nil || host != c.host || port != c.port {
			t.Errorf("%q = (%q, %d, %v), ожидалось (%q, %d)", c.in, host, port, err, c.host, c.port)
		}
	}
}

func TestFindByName(t *testing.T) {
	devices := []airplay.AirPlayDevice{
		{Name: "Телевизор", IP: "10.0.0.2"},
		{Name: "Кухня", IP: "10.0.0.3"},
		{Name: "Кухня", IP: "10.0.0.4"},
	}
	if d, err := findByName(devices, "Телевизор"); err != nil || d.IP != "10.0.0.2" {
		t.Errorf("точное имя: %v %v", d, err)
	}
	if d, err := findByName(devices, "телевизор"); err != nil || d.IP != "10.0.0.2" {
		t.Errorf("регистр: %v %v", d, err)
	}
	if _, err := findByName(devices, "Кухня"); err == nil || !strings.Contains(err.Error(), "неоднозначно") {
		t.Errorf("дубликаты должны давать ошибку, получено %v", err)
	}
	if _, err := findByName(devices, "Спальня"); err == nil {
		t.Error("отсутствующее имя должно давать ошибку")
	}
}

func TestHasNAL(t *testing.T) {
	au := []byte{0, 0, 0, 1, 0x67, 1, 2, 0, 0, 0, 1, 0x68, 3, 0, 0, 1, 0x65, 4}
	for typ, want := range map[byte]bool{7: true, 8: true, 5: true, 1: false} {
		if got := hasNAL(au, typ); got != want {
			t.Errorf("hasNAL(%d) = %v, ожидалось %v", typ, got, want)
		}
	}
}

type fakeSource struct {
	units []airplay.VideoAccessUnit
}

func (f *fakeSource) ReadAccessUnit(context.Context) (airplay.VideoAccessUnit, error) {
	if len(f.units) == 0 {
		return airplay.VideoAccessUnit{}, io.EOF
	}
	u := f.units[0]
	f.units = f.units[1:]
	return u, nil
}
func (f *fakeSource) Close() error { return nil }

func TestPrimeSourceReturnsFirstFrameThenRest(t *testing.T) {
	idr := []byte{0, 0, 0, 1, 0x67, 1, 0, 0, 0, 1, 0x68, 2, 0, 0, 0, 1, 0x65, 3}
	p := []byte{0, 0, 0, 1, 0x41, 4}
	src, err := primeSource(context.Background(), &fakeSource{units: []airplay.VideoAccessUnit{
		{AnnexB: idr, PTS: time.Unix(1, 0)}, {AnnexB: p, PTS: time.Unix(2, 0)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := src.ReadAccessUnit(context.Background())
	b, _ := src.ReadAccessUnit(context.Background())
	if len(a.AnnexB) != len(idr) || len(b.AnnexB) != len(p) {
		t.Fatalf("порядок кадров нарушен: %d, %d", len(a.AnnexB), len(b.AnnexB))
	}
	if _, err := src.ReadAccessUnit(context.Background()); err != io.EOF {
		t.Fatalf("после кадров ожидался EOF, получено %v", err)
	}
}

func TestPrimeSourceRejectsFrameWithoutParameterSets(t *testing.T) {
	_, err := primeSource(context.Background(), &fakeSource{units: []airplay.VideoAccessUnit{
		{AnnexB: []byte{0, 0, 0, 1, 0x41, 4}},
	}})
	if err == nil {
		t.Fatal("кадр без SPS/PPS/IDR должен отвергаться")
	}
}

func TestPrintDevices(t *testing.T) {
	var sb strings.Builder
	printDevices(&sb, []airplay.AirPlayDevice{{
		Name: "Телевизор", IP: "192.168.1.50", Port: 7000, Model: "AppleTV3,2",
		Features: 0x527ffee6, SourceVersion: "220.68", DeviceID: "F2:B1:C4:23:11:AF",
	}})
	out := sb.String()
	for _, want := range []string{`имя="Телевизор"`, "адрес=192.168.1.50", "порт=7000", `модель="AppleTV3,2"`, "features=0x527ffee6", "зеркалирование=да"} {
		if !strings.Contains(out, want) {
			t.Errorf("в выводе list нет %q: %s", want, out)
		}
	}
}
