// Пакет dxgi — захват рабочего стола через DXGI Desktop Duplication и
// подготовка кадра для кодера (BGRA -> NV12 с масштабированием). Этот файл и
// convert.go, cursor.go не зависят от Windows и проверяются обычными тестами.
package dxgi

// Rect — прямоугольник в пикселях.
type Rect struct {
	X, Y, W, H int
}

// Right и Bottom — правая и нижняя границы (не включительно).
func (r Rect) Right() int  { return r.X + r.W }
func (r Rect) Bottom() int { return r.Y + r.H }

// FitRect вписывает кадр srcW x srcH в холст dstW x dstH с сохранением
// пропорций и центрированием; остальное — чёрные поля. Размеры и смещение
// чётные, потому что цветность NV12 хранится для блоков 2x2.
func FitRect(srcW, srcH, dstW, dstH int) Rect {
	if srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0 {
		return Rect{W: dstW &^ 1, H: dstH &^ 1}
	}
	// Сравниваем srcW/srcH и dstW/dstH без деления: шире исходник — упираемся в ширину.
	var w, h int
	if srcW*dstH >= srcH*dstW {
		w = dstW
		h = (dstW*srcH + srcW/2) / srcW
	} else {
		h = dstH
		w = (dstH*srcW + srcH/2) / srcH
	}
	w &^= 1
	h &^= 1
	if w < 2 {
		w = 2
	}
	if h < 2 {
		h = 2
	}
	if w > dstW&^1 {
		w = dstW &^ 1
	}
	if h > dstH&^1 {
		h = dstH &^ 1
	}
	return Rect{X: ((dstW - w) / 2) &^ 1, Y: ((dstH - h) / 2) &^ 1, W: w, H: h}
}

// NV12Size — размер кадра NV12 без выравнивания строк: плоскость Y и
// чередующиеся Cb/Cr вдвое меньшей высоты.
func NV12Size(w, h int) int { return w * h * 3 / 2 }
