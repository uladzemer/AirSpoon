//go:build windows && (amd64 || arm64)

package mf

import (
	"sync"
	"testing"
	"time"

	"airspoon/internal/capture/dxgi"
	"airspoon/internal/win/com"
)

// syntheticNV12 рисует кадр с движущимся градиентом: кодеру есть что кодировать.
func syntheticNV12(w, h, n int) []byte {
	data := make([]byte, w*h*3/2)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			data[y*w+x] = byte(16 + (x+3*n)%200)
		}
	}
	uv := data[w*h:]
	for i := 0; i < len(uv); i += 2 {
		uv[i], uv[i+1] = byte(100+n%50), byte(140-n%40)
	}
	return data
}

type collector struct {
	mu      sync.Mutex
	packets []Packet
}

func (c *collector) add(p Packet) {
	c.mu.Lock()
	c.packets = append(c.packets, p)
	c.mu.Unlock()
}

func (c *collector) snapshot() []Packet {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Packet(nil), c.packets...)
}

func testConfig(t *testing.T, out func(Packet)) Config {
	return Config{Width: 1280, Height: 720, FPS: 30, BitrateKbps: 2500, Output: out, Logf: t.Logf, Debugf: t.Logf}
}

func runEncoder(t *testing.T, enc *Encoder, col *collector) {
	t.Helper()
	t.Logf("кодер: %q, аппаратный=%t, текстуры=%t", enc.Name(), enc.Hardware(), enc.Textures())
	const frames = 75
	interval := time.Second / 30
	next := time.Now()
	accepted := 0
	for i := 0; i < frames; i++ {
		ok, err := enc.EncodeNV12(syntheticNV12(1280, 720, i), int64(i)*10_000_000/30)
		if err != nil {
			t.Fatalf("кадр %d: %v", i, err)
		}
		if ok {
			accepted++
		}
		next = next.Add(interval)
		time.Sleep(time.Until(next))
	}
	// Дадим кодеру закончить выдачу.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && uint64(len(col.snapshot())) < enc.Inputs() {
		time.Sleep(20 * time.Millisecond)
	}
	pk := col.snapshot()
	t.Logf("принято %d кадров, получено %d пакетов", accepted, len(pk))
	if accepted < frames/2 {
		t.Fatalf("кодер принял слишком мало кадров: %d из %d", accepted, frames)
	}
	if len(pk) < accepted*8/10 {
		t.Fatalf("пакетов %d при принятых кадрах %d", len(pk), accepted)
	}
	first := pk[0]
	if !first.Key {
		t.Fatal("первый кадр потока должен быть IDR")
	}
	// Профиль в SPS: байт profile_idc сразу за заголовком NAL.
	for _, n := range splitAnnexB(first.AnnexB) {
		if n[0]&0x1f == nalSPS && len(n) > 3 {
			t.Logf("SPS: profile_idc=%d, level_idc=%d", n[1], n[3])
			if n[1] != h264ProfileMain {
				t.Errorf("профиль потока %d, ожидался Main (%d)", n[1], h264ProfileMain)
			}
		}
	}
	has := map[byte]bool{}
	for _, n := range splitAnnexB(first.AnnexB) {
		has[n[0]&0x1f] = true
	}
	if !has[nalSPS] || !has[nalPPS] || !has[nalIDR] {
		t.Fatalf("первый кадр без SPS/PPS/IDR: %v", has)
	}
	keys, total := 0, 0
	for _, p := range pk {
		total += len(p.AnnexB)
		if p.Key {
			keys++
			n := map[byte]bool{}
			for _, nal := range splitAnnexB(p.AnnexB) {
				n[nal[0]&0x1f] = true
			}
			if !n[nalSPS] || !n[nalPPS] {
				t.Errorf("IDR без SPS/PPS в пакете со временем %d", p.SampleTime)
			}
		}
	}
	for i := 1; i < len(pk); i++ {
		if pk[i].SampleTime <= pk[i-1].SampleTime {
			t.Fatalf("порядок выдачи нарушен (B-кадры?): %d после %d", pk[i].SampleTime, pk[i-1].SampleTime)
		}
	}
	t.Logf("IDR: %d, средний размер кадра %d байт, ~%d кбит/с", keys, total/len(pk), total*8/1000*30/len(pk))
}

func TestSoftwareEncoder(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	col := &collector{}
	enc, err := NewSoftware(testConfig(t, col.add))
	if err != nil {
		t.Skipf("программный кодер H.264 недоступен: %v", err)
	}
	defer enc.Close()
	runEncoder(t, enc, col)
}

func TestHardwareEncoderSystemMemory(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	col := &collector{}
	enc, err := NewHardware(testConfig(t, col.add))
	if err != nil {
		t.Skipf("аппаратный кодер H.264 недоступен: %v", err)
	}
	defer enc.Close()
	runEncoder(t, enc, col)
}

// ForceKeyFrame должен давать IDR через несколько кадров (с SPS и PPS), а не
// ждать конца GOP.
func testForcedKeyFrame(t *testing.T, enc *Encoder, col *collector) {
	t.Helper()
	// Прогреваем кодер, чтобы первый IDR остался позади и до следующего по GOP было далеко.
	for i := 0; i < 15; i++ {
		if _, err := enc.EncodeNV12(syntheticNV12(1280, 720, i), int64(i)*10_000_000/30); err != nil {
			t.Fatal(err)
		}
		time.Sleep(33 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	before := len(col.snapshot())
	enc.ForceKeyFrame()
	for i := 15; i < 30; i++ {
		if _, err := enc.EncodeNV12(syntheticNV12(1280, 720, i), int64(i)*10_000_000/30); err != nil {
			t.Fatal(err)
		}
		time.Sleep(33 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	for _, p := range col.snapshot()[before:] {
		if p.Key {
			return
		}
	}
	t.Fatal("после ForceKeyFrame IDR не появился за 15 кадров")
}

func TestSoftwareEncoderForcedKeyFrame(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	col := &collector{}
	enc, err := NewSoftware(testConfig(t, col.add))
	if err != nil {
		t.Skipf("программный кодер H.264 недоступен: %v", err)
	}
	defer enc.Close()
	testForcedKeyFrame(t, enc, col)
}

func TestHardwareEncoderForcedKeyFrame(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	col := &collector{}
	enc, err := NewHardware(testConfig(t, col.add))
	if err != nil {
		t.Skipf("аппаратный кодер H.264 недоступен: %v", err)
	}
	defer enc.Close()
	testForcedKeyFrame(t, enc, col)
}

func TestEnumAdapterLUIDAttributeGUID(t *testing.T) {
	// mfapi.h: DEFINE_GUID(MFT_ENUM_ADAPTER_LUID, 0x1d39518c, 0xe220, 0x4da8, 0xa0,0x7f, 0xba,0x17,0x25,0x52,0xd6,0xb1)
	want := com.GUID{Data1: 0x1d39518c, Data2: 0xe220, Data3: 0x4da8, Data4: [8]byte{0xa0, 0x7f, 0xba, 0x17, 0x25, 0x52, 0xd6, 0xb1}}
	if attrEnumAdapterLUID != want {
		t.Fatalf("MFT_ENUM_ADAPTER_LUID = %v, ожидали %v", attrEnumAdapterLUID, want)
	}
}

// realAdapterLUIDs возвращает LUID настоящих видеокарт этого компьютера.
func realAdapterLUIDs(t *testing.T) []AdapterLUID {
	t.Helper()
	list, err := dxgi.ListAdapters()
	if err != nil || len(list) == 0 {
		t.Skipf("видеокарты недоступны: %v", err)
	}
	var out []AdapterLUID
	for _, a := range list {
		if !a.Software() && a.VendorID != 0x1414 {
			out = append(out, AdapterLUID{Low: a.LUID.Low, High: a.LUID.High})
		}
	}
	if len(out) == 0 {
		t.Skip("настоящих видеокарт нет")
	}
	return out
}

// Кодеры ищутся по LUID видеокарты: список для каждой видеокарты — подмножество общего.
func TestHardwareEncoderNamesPerAdapter(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	all, err := HardwareEncoderNames(nil)
	if err != nil {
		t.Skipf("перечисление кодеров недоступно: %v", err)
	}
	t.Logf("все аппаратные кодеры H.264: %v", all)
	for _, l := range realAdapterLUIDs(t) {
		l := l
		names, err := HardwareEncoderNames(&l)
		t.Logf("видеокарта %s: кодеры %v (ошибка: %v)", l.String(), names, err)
		if err != nil {
			continue
		}
		for _, n := range names {
			found := false
			for _, a := range all {
				found = found || a == n
			}
			if !found {
				t.Errorf("кодер %q видеокарты %s отсутствует в общем списке", n, l.String())
			}
		}
	}
}

// Кодирование на видеокарте, найденной по LUID, и запасной путь для LUID, у
// которого кодеров нет: любой аппаратный кодер через системную память.
func TestHardwareEncoderByAdapterLUID(t *testing.T) {
	cleanup, err := com.Init()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	luid := realAdapterLUIDs(t)[0]
	for name, l := range map[string]AdapterLUID{"настоящий": luid, "несуществующий": {Low: 0xDEAD, High: 7}} {
		l := l
		t.Run(name, func(t *testing.T) {
			col := &collector{}
			cfg := testConfig(t, col.add)
			cfg.AdapterLUID = &l
			enc, err := NewHardware(cfg)
			if err != nil {
				t.Skipf("аппаратный кодер H.264 недоступен: %v", err)
			}
			defer enc.Close()
			runEncoder(t, enc, col)
		})
	}
}
