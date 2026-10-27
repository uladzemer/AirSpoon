package main

import (
	"image"
	"math"
	"testing"
)

// stroke — покрытие рамки вдоль линии: sum — толщина в пикселях, peak — самый
// плотный пиксель (чёткая линия имеет peak 1, размытая по двум пикселям — меньше).
// horizontal=true — идём по строке fixed, иначе по столбцу fixed.
func stroke(img *image.NRGBA, fixed, from, to int, horizontal bool) (sum, peak float64) {
	for i := from; i < to; i++ {
		x, y := i, fixed
		if !horizontal {
			x, y = fixed, i
		}
		a := float64(img.NRGBAAt(x, y).A) / 255
		sum += a
		peak = math.Max(peak, a)
	}
	return sum, peak
}

// На всех размерах четыре линии большого экрана (верх, низ, лево, право) должны
// быть одинаково чёткими: раньше на 24 px верх и лево ложились на сетку пикселей, а
// низ и право (толщина 1,5 px) размазывались по двум пикселям и выходили светлее.
func TestBigScreenStrokesAreSymmetric(t *testing.T) {
	for _, size := range []int{16, 20, 24, 32, 40, 48, 64} {
		img := render(size, monoPalette(colWhite, false))
		s := float64(size) / 1024
		at := func(v float64) int { return int(math.Round(v * s)) }
		// Вариант передатчика зеркален: рамка занимает x 256..896, y 256..672;
		// малый экран и вырез — слева внизу.
		cx := at(700) // столбец правее выреза: верх и низ рамки
		cy := at(380) // строка выше выреза: левая и правая стороны
		names := []string{"верх", "низ", "лево", "право"}
		var sums, peaks [4]float64
		sums[0], peaks[0] = stroke(img, cx, at(256)-2, at(480), false)
		sums[1], peaks[1] = stroke(img, cx, at(480), at(672)+3, false)
		sums[2], peaks[2] = stroke(img, cy, at(256)-2, at(480), true)
		sums[3], peaks[3] = stroke(img, cy, at(480), at(896)+3, true)
		for i := 1; i < 4; i++ {
			if math.Abs(sums[i]-sums[0]) > 0.02 {
				t.Errorf("%d px: толщина «%s» %.2f, верха %.2f", size, names[i], sums[i], sums[0])
			}
			if math.Abs(peaks[i]-peaks[0]) > 0.02 {
				t.Errorf("%d px: самый плотный пиксель «%s» %.2f, верха %.2f: линия размыта", size, names[i], peaks[i], peaks[0])
			}
		}
		if snapSizes[size] || size == 16 {
			for i, p := range peaks {
				if p < 0.98 {
					t.Errorf("%d px: линия «%s» не чёткая (peak %.2f)", size, names[i], p)
				}
			}
		}
	}
}
