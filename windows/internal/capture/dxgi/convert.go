package dxgi

import (
	"runtime"
	"sync"
)

// Коэффициенты BGRA -> YCbCr по BT.709, ограниченный диапазон (Y 16..235,
// Cb/Cr 16..240), масштаб 1/256. Те же матрица и диапазон задаются
// видеопроцессору GPU и тегам потока, чтобы картинка не зависела от пути.
const (
	yR, yG, yB    = 47, 157, 16
	cbR, cbG, cbB = -26, -87, 113
	crR, crG, crB = 112, -102, -10
)

// FillBlackNV12 заливает кадр NV12 чёрным (Y=16, Cb=Cr=128) для строк без выравнивания.
func FillBlackNV12(dst []byte, w, h int) {
	ySize := w * h
	for i := 0; i < ySize && i < len(dst); i++ {
		dst[i] = 16
	}
	for i := ySize; i < NV12Size(w, h) && i < len(dst); i++ {
		dst[i] = 128
	}
}

// ConvertBGRAToNV12 масштабирует кадр BGRA (srcW x srcH, строка srcStride байт)
// в прямоугольник fit внутри холста dstW x dstH формата NV12 (строки без
// выравнивания: stride = dstW) и переводит цвет. Участок вне fit — чёрные поля.
// Масштабирование — усреднение по ячейке исходника, соответствующей целевому
// пикселю. fit должен быть чётным (см. FitRect).
func ConvertBGRAToNV12(dst []byte, dstW, dstH int, src []byte, srcStride, srcW, srcH int, fit Rect) {
	if len(dst) < NV12Size(dstW, dstH) || srcW <= 0 || srcH <= 0 || fit.W <= 0 || fit.H <= 0 {
		return
	}
	if fit.X != 0 || fit.Y != 0 || fit.W != dstW || fit.H != dstH {
		FillBlackNV12(dst, dstW, dstH)
	}
	xs := make([]int, fit.W+1)
	for i := range xs {
		xs[i] = i * srcW / fit.W
	}
	ys := make([]int, fit.H+1)
	for i := range ys {
		ys[i] = i * srcH / fit.H
	}

	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	// Полосы по парам строк: чётная высота полосы сохраняет границы блоков 2x2.
	pairs := fit.H / 2
	if workers > pairs {
		workers = pairs
	}
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		p0 := pairs * w / workers
		p1 := pairs * (w + 1) / workers
		wg.Add(1)
		go func() {
			defer wg.Done()
			convertBand(dst, dstW, dstH, src, srcStride, fit, xs, ys, 2*p0, 2*p1)
		}()
	}
	wg.Wait()
}

func convertBand(dst []byte, dstW, dstH int, src []byte, srcStride int, fit Rect, xs, ys []int, y0, y1 int) {
	uvBase := dstW * dstH
	for dy := y0; dy < y1; dy += 2 {
		for dx := 0; dx < fit.W; dx += 2 {
			var sr, sg, sb int
			for j := 0; j < 2; j++ {
				sy0, sy1 := ys[dy+j], ys[dy+j+1]
				if sy1 <= sy0 {
					sy1 = sy0 + 1
				}
				row := (fit.Y+dy+j)*dstW + fit.X + dx
				for i := 0; i < 2; i++ {
					sx0, sx1 := xs[dx+i], xs[dx+i+1]
					if sx1 <= sx0 {
						sx1 = sx0 + 1
					}
					var r, g, b int
					for sy := sy0; sy < sy1; sy++ {
						p := sy*srcStride + sx0*4
						for sx := sx0; sx < sx1; sx++ {
							b += int(src[p])
							g += int(src[p+1])
							r += int(src[p+2])
							p += 4
						}
					}
					n := (sx1 - sx0) * (sy1 - sy0)
					if n > 1 {
						r, g, b = r/n, g/n, b/n
					}
					dst[row+i] = clampByte(((yR*r + yG*g + yB*b + 128) >> 8) + 16)
					sr += r
					sg += g
					sb += b
				}
			}
			sr, sg, sb = sr/4, sg/4, sb/4
			uv := uvBase + (fit.Y+dy)/2*dstW + fit.X + dx
			dst[uv] = clampByte(((cbR*sr + cbG*sg + cbB*sb + 128) >> 8) + 128)
			dst[uv+1] = clampByte(((crR*sr + crG*sg + crB*sb + 128) >> 8) + 128)
		}
	}
}

func clampByte(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}
