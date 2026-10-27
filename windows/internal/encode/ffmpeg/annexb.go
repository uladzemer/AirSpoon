// Пакет ffmpeg — временный источник видео для AirSpoon (веха 1): запускает
// ffmpeg.exe с захватом экрана ddagrab и кодированием libx264 и отдаёт
// готовые access unit'ы H.264 в формате Annex-B. Позже заменяется на
// DXGI + Media Foundation (internal/encode/mf), интерфейс airplay.VideoSource
// остаётся тем же.
package ffmpeg

import (
	"time"

	"airspoon/internal/airplay"
)

// maxAccessUnitBytes ограничивает размер одной картинки: больше — признак
// повреждённого потока.
const maxAccessUnitBytes = 16 << 20

// assembler собирает поток Annex-B (без внешних границ кадров) в access unit'ы.
//
// Граница кадра определяется по NAL-единицам: AUD, SPS, PPS и SEI всегда
// открывают новый кадр, а слайс с first_mb_in_slice == 0 открывает новый кадр,
// если в текущем уже есть слайсы. Кадр отдаётся, когда началась следующая NAL
// следующего кадра, поэтому задержка равна одному интервалу кадров.
type assembler struct {
	buf      []byte    // с начала открытой NAL (вместе со стартовым кодом)
	open     bool      // buf начинается со стартового кода
	openTime time.Time // когда впервые увидели стартовый код открытой NAL
	scanFrom int       // откуда искать следующий стартовый код

	cur      []byte    // накапливаемый кадр
	curTime  time.Time // время первой NAL кадра
	curHasVC bool      // в кадре уже есть VCL-слайс

	out []airplay.VideoAccessUnit
}

// push принимает очередной кусок потока и возвращает готовые кадры.
// now — момент получения куска (метка времени для NAL, чей старт в нём найден).
func (a *assembler) push(data []byte, now time.Time) []airplay.VideoAccessUnit {
	a.buf = append(a.buf, data...)
	for {
		from := a.scanFrom
		if a.open {
			if minFrom := startCodeLen(a.buf); from < minFrom {
				from = minFrom
			}
		} else if from < 0 {
			from = 0
		}
		s := findStart(a.buf, from)
		if s < 0 {
			// Хвост из последних трёх байт мог быть началом стартового кода.
			a.scanFrom = len(a.buf) - 3
			if a.scanFrom < 0 {
				a.scanFrom = 0
			}
			if !a.open && len(a.buf) > 3 {
				// Мусор до первого стартового кода не нужен.
				a.buf = append(a.buf[:0], a.buf[len(a.buf)-3:]...)
				a.scanFrom = 0
			}
			break
		}
		if a.open {
			a.completeNAL(a.buf[:s], a.openTime)
		}
		// Сдвигаем буфер, не накапливая память.
		a.buf = append(a.buf[:0], a.buf[s:]...)
		a.open = true
		a.openTime = now
		a.scanFrom = startCodeLen(a.buf)
	}
	res := a.out
	a.out = nil
	return res
}

// finish вызывается на конце потока: закрывает последнюю NAL и последний кадр.
func (a *assembler) finish() []airplay.VideoAccessUnit {
	if a.open && len(a.buf) > 0 {
		a.completeNAL(a.buf, a.openTime)
	}
	a.buf, a.open = nil, false
	a.emit()
	res := a.out
	a.out = nil
	return res
}

func (a *assembler) emit() {
	if len(a.cur) > 0 && a.curHasVC {
		a.out = append(a.out, airplay.VideoAccessUnit{AnnexB: a.cur, PTS: a.curTime})
	}
	a.cur = nil
	a.curHasVC = false
}

func (a *assembler) completeNAL(nal []byte, t time.Time) {
	hdr := startCodeLen(nal)
	if len(nal) <= hdr {
		return
	}
	typ := nal[hdr] & 0x1f
	switch typ {
	case 9, 7, 8, 6: // AUD, SPS, PPS, SEI открывают новый кадр, если предыдущий уже с картинкой
		if a.curHasVC {
			a.emit()
		}
	case 1, 5:
		// first_mb_in_slice = ue(0) = бит «1» сразу после заголовка NAL.
		if a.curHasVC && len(nal) > hdr+1 && nal[hdr+1]&0x80 != 0 {
			a.emit()
		}
	}
	if len(a.cur) == 0 {
		a.curTime = t
	}
	if len(a.cur)+len(nal) > maxAccessUnitBytes {
		// Повреждённый поток: сбрасываем накопленное.
		a.cur = nil
		a.curHasVC = false
		return
	}
	a.cur = append(a.cur, nal...)
	if typ == 1 || typ == 5 {
		a.curHasVC = true
	}
}

// startCodeLen возвращает длину стартового кода в начале b (4, 3 или 0).
func startCodeLen(b []byte) int {
	if len(b) >= 4 && b[0] == 0 && b[1] == 0 && b[2] == 0 && b[3] == 1 {
		return 4
	}
	if len(b) >= 3 && b[0] == 0 && b[1] == 0 && b[2] == 1 {
		return 3
	}
	return 0
}

// findStart ищет стартовый код (00 00 01 или 00 00 00 01) начиная с from и
// возвращает индекс его первого байта.
func findStart(b []byte, from int) int {
	if from < 0 {
		from = 0
	}
	for i := from; i+2 < len(b); i++ {
		if b[i] != 0 || b[i+1] != 0 {
			continue
		}
		if b[i+2] == 1 {
			return i
		}
		if b[i+2] == 0 && i+3 < len(b) && b[i+3] == 1 {
			return i
		}
	}
	return -1
}
