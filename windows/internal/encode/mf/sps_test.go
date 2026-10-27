package mf

import (
	"strings"
	"testing"
)

// bitWriter собирает SPS для тестов (обратная операция к bitReader).
type bitWriter struct {
	b   []byte
	pos int
}

func (w *bitWriter) bit(v uint32) {
	if w.pos&7 == 0 {
		w.b = append(w.b, 0)
	}
	if v != 0 {
		w.b[len(w.b)-1] |= 1 << (7 - uint(w.pos&7))
	}
	w.pos++
}

func (w *bitWriter) u(n int, v uint32) {
	for i := n - 1; i >= 0; i-- {
		w.bit((v >> uint(i)) & 1)
	}
}

func (w *bitWriter) ue(v uint32) {
	v++
	n := 0
	for t := v; t > 1; t >>= 1 {
		n++
	}
	w.u(n, 0)
	w.u(n+1, v)
}

func (w *bitWriter) se(v int32) {
	if v > 0 {
		w.ue(uint32(2*v - 1))
	} else {
		w.ue(uint32(-2 * v))
	}
}

// escape вставляет байты предотвращения эмуляции (00 00 0x -> 00 00 03 0x).
func escape(b []byte) []byte {
	var out []byte
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

type spsSpec struct {
	profile, level int
	pocType        int
	numRef         int
	wMbs, hMbs     int
	cropBottom     int // в единицах обрезки
	vui            bool
	reorder        int // num_reorder_frames
	restriction    bool
	hrd            bool
	scaling        bool
}

func buildSPS(sp spsSpec) []byte {
	w := &bitWriter{}
	w.u(8, uint32(sp.profile))
	w.u(8, 0)
	w.u(8, uint32(sp.level))
	w.ue(0)
	if sp.profile == 100 {
		w.ue(1) // chroma_format_idc
		w.ue(0)
		w.ue(0)
		w.u(1, 0)
		if sp.scaling {
			w.u(1, 1)
			for i := 0; i < 8; i++ {
				if i == 0 || i == 6 {
					w.u(1, 1)
					size := 16
					if i >= 6 {
						size = 64
					}
					for j := 0; j < size; j++ {
						w.se(1) // дельта; список не заканчивается нулём
					}
				} else {
					w.u(1, 0)
				}
			}
		} else {
			w.u(1, 0)
		}
	}
	w.ue(4) // log2_max_frame_num_minus4
	w.ue(uint32(sp.pocType))
	switch sp.pocType {
	case 0:
		w.ue(4)
	case 1:
		w.u(1, 0)
		w.se(-3)
		w.se(2)
		w.ue(2)
		w.se(1)
		w.se(-1)
	}
	w.ue(uint32(sp.numRef))
	w.u(1, 0)
	w.ue(uint32(sp.wMbs - 1))
	w.ue(uint32(sp.hMbs - 1))
	w.u(1, 1) // frame_mbs_only
	w.u(1, 1) // direct_8x8
	if sp.cropBottom > 0 {
		w.u(1, 1)
		w.ue(0)
		w.ue(0)
		w.ue(0)
		w.ue(uint32(sp.cropBottom))
	} else {
		w.u(1, 0)
	}
	if !sp.vui {
		w.u(1, 0)
	} else {
		w.u(1, 1)
		w.u(1, 1) // aspect_ratio_info_present
		w.u(8, 255)
		w.u(16, 1)
		w.u(16, 1)
		w.u(1, 0) // overscan
		w.u(1, 1) // video_signal_type
		w.u(3, 5)
		w.u(1, 0)
		w.u(1, 1)
		w.u(24, 0x010101)
		w.u(1, 0) // chroma_loc
		w.u(1, 1) // timing_info
		w.u(32, 1001)
		w.u(32, 60000)
		w.u(1, 1)
		if sp.hrd {
			w.u(1, 1) // nal_hrd
			w.ue(0)
			w.u(4, 1)
			w.u(4, 2)
			w.ue(1000)
			w.ue(2000)
			w.u(1, 1)
			w.u(5, 23)
			w.u(5, 23)
			w.u(5, 23)
			w.u(5, 24)
			w.u(1, 0) // vcl_hrd
			w.u(1, 0) // low_delay
		} else {
			w.u(1, 0)
			w.u(1, 0)
		}
		w.u(1, 0) // pic_struct_present
		if sp.restriction {
			w.u(1, 1)
			w.u(1, 1)
			w.ue(0)
			w.ue(1)
			w.ue(16)
			w.ue(16)
			w.ue(uint32(sp.reorder))
			w.ue(uint32(sp.reorder + 1))
		} else {
			w.u(1, 0)
		}
	}
	w.u(1, 1) // rbsp_stop_one_bit
	return append([]byte{0x67}, escape(w.b)...)
}

func TestParseSPSHigh1080p(t *testing.T) {
	nal := buildSPS(spsSpec{profile: 100, level: 42, pocType: 0, numRef: 1, wMbs: 120, hMbs: 68, cropBottom: 4,
		vui: true, restriction: true, reorder: 0, hrd: true})
	s, err := ParseSPS(nal)
	if err != nil {
		t.Fatal(err)
	}
	if s.ProfileIdc != 100 || s.LevelIdc != 42 || s.Width != 1920 || s.Height != 1080 || s.PocType != 0 || s.MaxNumRef != 1 {
		t.Fatalf("разобрано неверно: %+v", s)
	}
	if yes, known := s.BFrames(); yes || !known {
		t.Fatalf("B-кадры: yes=%t known=%t, ожидали «нет, известно»: %+v", yes, known, s)
	}
	str := s.String()
	for _, want := range []string{"High", "4.2", "1920x1080", "B-кадров нет"} {
		if !strings.Contains(str, want) {
			t.Errorf("в %q нет %q", str, want)
		}
	}
}

func TestParseSPSBFramesFromVUI(t *testing.T) {
	nal := buildSPS(spsSpec{profile: 100, level: 41, pocType: 0, numRef: 4, wMbs: 80, hMbs: 45, vui: true, restriction: true, reorder: 2})
	s, err := ParseSPS(nal)
	if err != nil {
		t.Fatal(err)
	}
	if yes, known := s.BFrames(); !yes || !known || s.NumReorderFrames != 2 || s.Width != 1280 || s.Height != 720 {
		t.Fatalf("ожидали B-кадры из VUI (reorder=2): %+v", s)
	}
	if !strings.Contains(s.String(), "B-кадры: есть") {
		t.Fatalf("строка: %s", s)
	}
}

func TestParseSPSPocType2MeansNoBFrames(t *testing.T) {
	nal := buildSPS(spsSpec{profile: 66, level: 31, pocType: 2, numRef: 1, wMbs: 80, hMbs: 45})
	s, err := ParseSPS(nal)
	if err != nil {
		t.Fatal(err)
	}
	if yes, known := s.BFrames(); yes || !known {
		t.Fatalf("poc type 2: yes=%t known=%t", yes, known)
	}
	if s.ProfileName() != "Baseline" || s.LevelName() != "3.1" {
		t.Fatalf("%s %s", s.ProfileName(), s.LevelName())
	}
}

func TestParseSPSUnknownWithoutVUI(t *testing.T) {
	nal := buildSPS(spsSpec{profile: 77, level: 40, pocType: 0, numRef: 2, wMbs: 80, hMbs: 45})
	s, err := ParseSPS(nal)
	if err != nil {
		t.Fatal(err)
	}
	if _, known := s.BFrames(); known {
		t.Fatalf("без VUI и при poc type 0 B-кадры определить нельзя: %+v", s)
	}
	if !strings.Contains(s.String(), "по SPS не определить") {
		t.Fatalf("строка: %s", s)
	}
}

func TestParseSPSPocType1AndScalingLists(t *testing.T) {
	nal := buildSPS(spsSpec{profile: 100, level: 40, pocType: 1, numRef: 1, wMbs: 120, hMbs: 68, cropBottom: 4,
		scaling: true, vui: true, restriction: true, reorder: 0})
	s, err := ParseSPS(nal)
	if err != nil {
		t.Fatal(err)
	}
	if s.Width != 1920 || s.Height != 1080 || !s.HasBitstreamRestriction || s.NumReorderFrames != 0 {
		t.Fatalf("после списков масштабирования и poc type 1 разбор сбился: %+v", s)
	}
}

// Настоящий SPS x264, 1920x1080 High 4.0, с байтами предотвращения эмуляции.
func TestParseSPSReferenceX264(t *testing.T) {
	nal := []byte{0x67, 0x64, 0x00, 0x28, 0xac, 0xd9, 0x40, 0x78, 0x02, 0x27, 0xe5, 0xc0, 0x44, 0x00, 0x00, 0x03, 0x00, 0x04,
		0x00, 0x00, 0x03, 0x00, 0xc8, 0x3c, 0x60, 0xc6, 0x58}
	s, err := ParseSPS(nal)
	if err != nil {
		t.Fatal(err)
	}
	if s.ProfileIdc != 100 || s.LevelIdc != 40 || s.Width != 1920 || s.Height != 1080 {
		t.Fatalf("эталон разобран неверно: %+v", s)
	}
}

func TestParseSPSRejectsBadInput(t *testing.T) {
	if _, err := ParseSPS([]byte{0x68, 1, 2, 3, 4}); err == nil {
		t.Fatal("PPS принят за SPS")
	}
	if _, err := ParseSPS([]byte{0x67, 0x64}); err == nil {
		t.Fatal("короткий SPS принят")
	}
	full := buildSPS(spsSpec{profile: 100, level: 42, pocType: 0, numRef: 1, wMbs: 120, hMbs: 68})
	if _, err := ParseSPS(full[:6]); err == nil {
		t.Fatal("обрезанный SPS принят")
	}
}

func TestUnescapeRBSP(t *testing.T) {
	got := unescapeRBSP([]byte{1, 0, 0, 3, 1, 0, 0, 3, 0, 0, 3})
	want := []byte{1, 0, 0, 1, 0, 0, 0, 0}
	if string(got) != string(want) {
		t.Fatalf("получили %v, ожидали %v", got, want)
	}
}

// SPS из тестов annexb (testSPS) тоже должен разбираться и не падать.
func TestParseSPSTestFixture(t *testing.T) {
	if s, err := ParseSPS(testSPS); err != nil || s.ProfileIdc != 77 || s.LevelIdc != 31 {
		t.Fatalf("%+v %v", s, err)
	}
}
