package mf

import (
	"errors"
	"fmt"
)

// SPSInfo — разобранные поля набора параметров последовательности (SPS), которые
// важны для совместимости с приёмником: профиль, уровень, размер и признаки
// B-кадров. Нужен только для журнала и диагностики.
type SPSInfo struct {
	ProfileIdc    int
	ConstraintSet int // байт constraint_set0..5_flag + reserved_zero_2bits
	LevelIdc      int
	ChromaFormat  int
	Width, Height int // с учётом обрезки
	PocType       int
	MaxNumRef     int
	FrameMbsOnly  bool
	// HasBitstreamRestriction — в VUI есть bitstream_restriction: тогда
	// NumReorderFrames достоверно говорит о B-кадрах.
	HasBitstreamRestriction bool
	NumReorderFrames        int
	MaxDecFrameBuffering    int
}

// BFrames сообщает, есть ли в потоке B-кадры (порядок вывода не равен порядку
// кодирования). known=false — по SPS это определить нельзя.
func (s SPSInfo) BFrames() (yes, known bool) {
	switch {
	case s.PocType == 2:
		// Тип 2: порядок вывода совпадает с порядком декодирования.
		return false, true
	case s.HasBitstreamRestriction:
		return s.NumReorderFrames > 0, true
	}
	return false, false
}

// ProfileName — «Main», «High» и т. д. (Constrained Baseline отмечается особо).
func (s SPSInfo) ProfileName() string {
	if s.ProfileIdc == 66 && s.ConstraintSet&0x40 != 0 {
		return "Constrained Baseline"
	}
	return h264ProfileName(s.ProfileIdc)
}

// LevelName — «4.1», «4.2», «1b».
func (s SPSInfo) LevelName() string {
	if s.LevelIdc == 11 && s.ConstraintSet&0x10 != 0 && (s.ProfileIdc == 66 || s.ProfileIdc == 77 || s.ProfileIdc == 88) {
		return "1b"
	}
	if s.LevelIdc == 9 {
		return "1b"
	}
	return h264LevelName(s.LevelIdc)
}

// BFramesText — признак B-кадров словами.
func (s SPSInfo) BFramesText() string {
	yes, known := s.BFrames()
	switch {
	case !known:
		return "B-кадры: по SPS не определить"
	case yes:
		return fmt.Sprintf("B-кадры: есть (num_reorder_frames=%d)", s.NumReorderFrames)
	case s.HasBitstreamRestriction:
		return "B-кадров нет (num_reorder_frames=0)"
	}
	return "B-кадров нет (pic_order_cnt_type=2)"
}

// String — одна строка для журнала.
func (s SPSInfo) String() string {
	return fmt.Sprintf("SPS: профиль %s (profile_idc=%d), уровень %s (level_idc=%d), %dx%d, ссылочных кадров %d, %s",
		s.ProfileName(), s.ProfileIdc, s.LevelName(), s.LevelIdc, s.Width, s.Height, s.MaxNumRef, s.BFramesText())
}

var errSPSShort = errors.New("SPS обрезан")

// bitReader читает биты MSB-first; выход за данные «залипает» в bad.
type bitReader struct {
	b   []byte
	pos int // в битах
	bad bool
}

func (r *bitReader) bit() uint32 {
	if r.pos>>3 >= len(r.b) {
		r.bad = true
		return 0
	}
	v := uint32(r.b[r.pos>>3]>>(7-uint(r.pos&7))) & 1
	r.pos++
	return v
}

func (r *bitReader) u(n int) uint32 {
	var v uint32
	for i := 0; i < n; i++ {
		v = v<<1 | r.bit()
	}
	return v
}

// ue читает число Exp-Golomb без знака.
func (r *bitReader) ue() uint32 {
	zeros := 0
	for r.bit() == 0 {
		if r.bad || zeros > 31 {
			r.bad = true
			return 0
		}
		zeros++
	}
	if zeros == 0 {
		return 0
	}
	return (1 << uint(zeros)) - 1 + r.u(zeros)
}

// se читает число Exp-Golomb со знаком.
func (r *bitReader) se() int32 {
	k := r.ue()
	if k&1 == 1 {
		return int32((k + 1) / 2)
	}
	return -int32(k / 2)
}

// unescapeRBSP убирает байты предотвращения эмуляции стартового кода (00 00 03 -> 00 00).
func unescapeRBSP(b []byte) []byte {
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
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

func skipScalingList(r *bitReader, size int) {
	last, next := 8, 8
	for j := 0; j < size && !r.bad; j++ {
		if next != 0 {
			next = (last + int(r.se()) + 256) % 256
		}
		if next != 0 {
			last = next
		}
	}
}

func skipHRD(r *bitReader) {
	cnt := int(r.ue()) + 1
	r.u(4)
	r.u(4)
	for i := 0; i < cnt && i < 32 && !r.bad; i++ {
		r.ue()
		r.ue()
		r.u(1)
	}
	r.u(5)
	r.u(5)
	r.u(5)
	r.u(5)
}

// ParseSPS разбирает NAL-единицу SPS (с заголовком NAL, без стартового кода).
func ParseSPS(nal []byte) (SPSInfo, error) {
	var s SPSInfo
	if len(nal) < 5 || nal[0]&0x1f != nalSPS {
		return s, errors.New("это не SPS")
	}
	r := &bitReader{b: unescapeRBSP(nal[1:])}
	s.ProfileIdc = int(r.u(8))
	s.ConstraintSet = int(r.u(8))
	s.LevelIdc = int(r.u(8))
	r.ue() // seq_parameter_set_id
	s.ChromaFormat = 1
	switch s.ProfileIdc {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		s.ChromaFormat = int(r.ue())
		if s.ChromaFormat == 3 {
			r.u(1) // separate_colour_plane_flag
		}
		r.ue() // bit_depth_luma_minus8
		r.ue() // bit_depth_chroma_minus8
		r.u(1) // qpprime_y_zero_transform_bypass_flag
		if r.u(1) == 1 {
			n := 8
			if s.ChromaFormat == 3 {
				n = 12
			}
			for i := 0; i < n && !r.bad; i++ {
				if r.u(1) == 1 {
					if i < 6 {
						skipScalingList(r, 16)
					} else {
						skipScalingList(r, 64)
					}
				}
			}
		}
	}
	r.ue() // log2_max_frame_num_minus4
	s.PocType = int(r.ue())
	switch s.PocType {
	case 0:
		r.ue() // log2_max_pic_order_cnt_lsb_minus4
	case 1:
		r.u(1)
		r.se()
		r.se()
		n := int(r.ue())
		for i := 0; i < n && i < 256 && !r.bad; i++ {
			r.se()
		}
	}
	s.MaxNumRef = int(r.ue())
	r.u(1) // gaps_in_frame_num_value_allowed_flag
	wMbs := int(r.ue()) + 1
	hMapUnits := int(r.ue()) + 1
	s.FrameMbsOnly = r.u(1) == 1
	if !s.FrameMbsOnly {
		r.u(1) // mb_adaptive_frame_field_flag
	}
	r.u(1) // direct_8x8_inference_flag
	frameMbs := 1
	if !s.FrameMbsOnly {
		frameMbs = 2
	}
	s.Width, s.Height = wMbs*16, hMapUnits*16*frameMbs
	if r.u(1) == 1 { // frame_cropping_flag
		l, rt, t, b := int(r.ue()), int(r.ue()), int(r.ue()), int(r.ue())
		cx, cy := 1, frameMbs
		switch s.ChromaFormat {
		case 1:
			cx, cy = 2, 2*frameMbs
		case 2:
			cx, cy = 2, frameMbs
		}
		s.Width -= cx * (l + rt)
		s.Height -= cy * (t + b)
	}
	if r.bad {
		return s, errSPSShort
	}
	if r.u(1) == 1 { // vui_parameters_present_flag
		parseVUI(r, &s)
	}
	// Ошибка в VUI не обесценивает остальное: профиль, уровень и размер уже верны.
	return s, nil
}

func parseVUI(r *bitReader, s *SPSInfo) {
	if r.u(1) == 1 { // aspect_ratio_info_present_flag
		if r.u(8) == 255 {
			r.u(16)
			r.u(16)
		}
	}
	if r.u(1) == 1 { // overscan_info_present_flag
		r.u(1)
	}
	if r.u(1) == 1 { // video_signal_type_present_flag
		r.u(3)
		r.u(1)
		if r.u(1) == 1 {
			r.u(24)
		}
	}
	if r.u(1) == 1 { // chroma_loc_info_present_flag
		r.ue()
		r.ue()
	}
	if r.u(1) == 1 { // timing_info_present_flag
		r.u(32)
		r.u(32)
		r.u(1)
	}
	nal := r.u(1) == 1
	if nal {
		skipHRD(r)
	}
	vcl := r.u(1) == 1
	if vcl {
		skipHRD(r)
	}
	if nal || vcl {
		r.u(1) // low_delay_hrd_flag
	}
	r.u(1)           // pic_struct_present_flag
	if r.u(1) == 1 { // bitstream_restriction_flag
		r.u(1)
		r.ue()
		r.ue()
		r.ue()
		r.ue()
		reorder := int(r.ue())
		maxDec := int(r.ue())
		if !r.bad {
			s.HasBitstreamRestriction = true
			s.NumReorderFrames = reorder
			s.MaxDecFrameBuffering = maxDec
		}
	}
}
