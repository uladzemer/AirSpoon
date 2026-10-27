// Пакет mf — кодер H.264 на Media Foundation (аппаратный асинхронный MFT и
// программный Microsoft H.264 Encoder MFT). Этот файл содержит разбор потока
// Annex-B и вставку наборов параметров SPS/PPS; он не зависит от Windows.
package mf

// Типы NAL-единиц H.264.
const (
	nalSlice = 1
	nalIDR   = 5
	nalSEI   = 6
	nalSPS   = 7
	nalPPS   = 8
	nalAUD   = 9
)

var startCode4 = []byte{0, 0, 0, 1}

// startCodeAt возвращает длину стартового кода (3 или 4) в позиции i или 0.
func startCodeAt(b []byte, i int) int {
	if i+3 <= len(b) && b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
		return 3
	}
	if i+4 <= len(b) && b[i] == 0 && b[i+1] == 0 && b[i+2] == 0 && b[i+3] == 1 {
		return 4
	}
	return 0
}

// hasStartCode проверяет, что данные начинаются со стартового кода Annex-B.
func hasStartCode(b []byte) bool { return startCodeAt(b, 0) != 0 }

// splitAnnexB разбивает поток Annex-B на NAL-единицы без стартовых кодов.
// Байты до первого стартового кода отбрасываются, нулевые байты в конце NAL
// (часть следующего четырёхбайтного кода) тоже.
func splitAnnexB(b []byte) [][]byte {
	var nals [][]byte
	start := -1
	for i := 0; i < len(b); {
		n := startCodeAt(b, i)
		if n == 0 {
			i++
			continue
		}
		if start >= 0 {
			nals = append(nals, trimTrailingZeros(b[start:i]))
		}
		start = i + n
		i += n
	}
	if start >= 0 && start < len(b) {
		nals = append(nals, trimTrailingZeros(b[start:]))
	}
	// Пустые NAL (подряд идущие стартовые коды) не нужны.
	out := nals[:0]
	for _, n := range nals {
		if len(n) > 0 {
			out = append(out, n)
		}
	}
	return out
}

func trimTrailingZeros(b []byte) []byte {
	for len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	return b
}

// avccToAnnexB переводит поток с четырёхбайтными длинами NAL (AVCC) в Annex-B.
// Возвращает false, если данные не похожи на AVCC.
func avccToAnnexB(b []byte) ([]byte, bool) {
	out := make([]byte, 0, len(b)+16)
	for i := 0; i < len(b); {
		if i+4 > len(b) {
			return nil, false
		}
		n := int(b[i])<<24 | int(b[i+1])<<16 | int(b[i+2])<<8 | int(b[i+3])
		i += 4
		if n <= 0 || i+n > len(b) {
			return nil, false
		}
		out = append(out, startCode4...)
		out = append(out, b[i:i+n]...)
		i += n
	}
	return out, len(out) > 0
}

// paramSets хранит последние SPS и PPS потока (без стартовых кодов).
type paramSets struct {
	sps, pps []byte
}

func (p *paramSets) ready() bool { return len(p.sps) > 0 && len(p.pps) > 0 }

// learn запоминает SPS и PPS из списка NAL-единиц.
func (p *paramSets) learn(nals [][]byte) {
	for _, n := range nals {
		switch n[0] & 0x1f {
		case nalSPS:
			p.sps = append(p.sps[:0], n...)
		case nalPPS:
			p.pps = append(p.pps[:0], n...)
		}
	}
}

// learnSequenceHeader разбирает MF_MT_MPEG_SEQUENCE_HEADER (SPS и PPS в Annex-B).
func (p *paramSets) learnSequenceHeader(blob []byte) {
	if hasStartCode(blob) {
		p.learn(splitAnnexB(blob))
	}
}

// prepare приводит access unit к виду, который ждёт VideoSource: Annex-B,
// а перед каждым IDR — SPS и PPS. Если кодер не положил их сам, берутся
// запомненные (из потока или из заголовка последовательности). key сообщает,
// что в кадре есть IDR-слайс; complete — что у IDR есть и SPS, и PPS.
func (p *paramSets) prepare(au []byte) (out []byte, key, complete bool) {
	if len(au) == 0 {
		return au, false, false
	}
	if !hasStartCode(au) {
		conv, ok := avccToAnnexB(au)
		if !ok {
			return au, false, false
		}
		au = conv
	}
	nals := splitAnnexB(au)
	var hasSPS, hasPPS bool
	for _, n := range nals {
		switch n[0] & 0x1f {
		case nalIDR:
			key = true
		case nalSPS:
			hasSPS = true
		case nalPPS:
			hasPPS = true
		}
	}
	p.learn(nals)
	if !key {
		return au, false, false
	}
	if hasSPS && hasPPS {
		return au, true, true
	}
	if !p.ready() {
		return au, true, false
	}
	// Собираем кадр заново: ведущие AUD, затем SPS и PPS (из этого кадра или
	// запомненные), затем остальные NAL без дублей наборов параметров.
	out = make([]byte, 0, len(au)+len(p.sps)+len(p.pps)+8)
	rest := nals
	for len(rest) > 0 && rest[0][0]&0x1f == nalAUD {
		out = append(append(out, startCode4...), rest[0]...)
		rest = rest[1:]
	}
	out = append(append(out, startCode4...), p.sps...)
	out = append(append(out, startCode4...), p.pps...)
	for _, n := range rest {
		if t := n[0] & 0x1f; t == nalSPS || t == nalPPS {
			continue
		}
		out = append(append(out, startCode4...), n...)
	}
	return out, true, true
}
