package mf

import "fmt"

// h264LevelLimits — ограничения уровней H.264 (таблица A-1 стандарта):
// maxMBPS — макроблоков в секунду, maxFS — макроблоков в кадре.
// В таблице только те уровни, которые мы реально выставляем: 4.1, 4.2, 5.1, 5.2.
// Уровень 5.1 покрывает и 5.0, поэтому 5.0 не нужен.
var h264LevelLimits = []struct {
	idc            int
	maxMBPS, maxFS int
}{
	{41, 245760, 8192},
	{42, 522240, 8704},
	{51, 983040, 36864},
	{52, 2073600, 36864},
}

// macroblocksPerSecond считает нагрузку на декодер: макроблоков 16x16 в кадре и в секунду.
func macroblocksPerSecond(w, h, fps int) (perFrame, perSecond int) {
	perFrame = ((w + 15) / 16) * ((h + 15) / 16)
	return perFrame, perFrame * fps
}

// chooseH264Level возвращает level_idc (41, 42, 51 или 52) — наименьший уровень, в
// который укладывается поток по размеру кадра и числу макроблоков в секунду.
// Раньше всегда ставился 4.1, что для 1080p60 (489600 МБ/с при пределе 245760)
// формально неверно: строгий приёмник мог отказаться от потока. Ниже 4.1 не опускаемся
// (так было и раньше, и приёмники это принимают). Если поток превосходит 5.2 — 52.
func chooseH264Level(w, h, fps int) int {
	perFrame, perSec := macroblocksPerSecond(w, h, fps)
	for _, l := range h264LevelLimits {
		if perFrame <= l.maxFS && perSec <= l.maxMBPS {
			return l.idc
		}
	}
	return h264LevelLimits[len(h264LevelLimits)-1].idc
}

// h264LevelName печатает level_idc как «4.1».
func h264LevelName(idc int) string {
	return fmt.Sprintf("%d.%d", idc/10, idc%10)
}

// h264ProfileName — название профиля по profile_idc.
func h264ProfileName(idc int) string {
	switch idc {
	case 66:
		return "Baseline"
	case 77:
		return "Main"
	case 88:
		return "Extended"
	case 100:
		return "High"
	case 110:
		return "High 10"
	case 122:
		return "High 4:2:2"
	case 244:
		return "High 4:4:4 Predictive"
	case 44:
		return "CAVLC 4:4:4"
	}
	return fmt.Sprintf("profile_idc=%d", idc)
}
