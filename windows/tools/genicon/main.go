// Команда genicon рисует значки AirSpoon Sender («Экран в экране», зеркальный
// вариант для передатчика) и записывает их в ICO-файлы. Исходные рисунки
// выбраны владельцем и лежат в plans/icons (s1-sender.svg, s1-mono-sender.svg,
// s1-16-sender.svg); здесь та же геометрия записана кодом, внешних библиотек и
// cgo не нужно: фигуры — функции расстояния (скруглённые прямоугольники) с
// суперсэмплингом, а 16 пикселей рисуются по целым координатам без сглаживания.
//
// Запуск из каталога windows:  go run ./tools/genicon [каталог]
//
// Результат (по умолчанию в assets):
//
//	airspoon.ico        цветной значок программы (exe, окна, ярлыки)
//	tray-white.ico      одноцветный белый знак для тёмной панели задач
//	tray-black.ico      одноцветный чёрный знак для светлой панели задач
//	tray-white-live.ico то же, но «идёт показ»: малый экран цветом #4F8CFF
//	tray-black-live.ico
//	airspoon.png, preview.png  предпросмотр для проверки глазами
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// Цвета выбранной иконки.
var (
	colBG     = color.NRGBA{0x10, 0x14, 0x18, 255}
	colMark   = color.NRGBA{0xF4, 0xF6, 0xF8, 255}
	colAccent = color.NRGBA{0x4F, 0x8C, 0xFF, 255}
	colWhite  = color.NRGBA{255, 255, 255, 255}
	colBlack  = color.NRGBA{0, 0, 0, 255}
)

// Размеры значка программы и значков трея. 16 рисуется по пикселям.
var (
	appSizes  = []int{16, 20, 24, 32, 40, 48, 64, 256}
	traySizes = []int{16, 20, 24, 32, 40, 48, 64}
)

// --- геометрия 1024x1024 (s1.svg) ---

// rrect — расстояние до скруглённого прямоугольника (x0,y0)-(x1,y1) с радиусом r:
// меньше нуля внутри.
func rrect(x0, y0, x1, y1, r float64) func(x, y float64) float64 {
	cx, cy := (x0+x1)/2, (y0+y1)/2
	hx, hy := (x1-x0)/2-r, (y1-y0)/2-r
	return func(x, y float64) float64 {
		qx, qy := math.Abs(x-cx)-hx, math.Abs(y-cy)-hy
		return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
	}
}

// Большой экран — рамка с обводкой 64 вокруг прямоугольника 160..736 x 288..640
// (rx 64): внешний радиус 96, внутренний 32. Малый экран — заливка 512..896 x
// 512..768 (rx 64). Вырез 480..960 x 480..816 (rx 96) оставляет зазор 32 вокруг
// малого экрана. Размеры из s1.svg.
var (
	bigOuter = rrect(128, 256, 768, 672, 96)
	bigInner = rrect(192, 320, 704, 608, 32)
	knockout = rrect(480, 480, 960, 816, 96)
	smallScr = rrect(512, 512, 896, 768, 64)
)

// geometry — четыре фигуры значка в системе 1024 (до зеркалирования).
type geometry struct {
	bigOuter, bigInner, knockout, smallScr func(x, y float64) float64
}

// Размеры, на которых геометрия 1024 не ложится на сетку пикселей: обводка 64
// даёт 1,25 px (20), 1,5 px (24) и 2,5 px (40), и верхняя/левая линии получаются
// чётче нижней/правой (дробная толщина сглаживается по-разному). Для них края
// фигур выравниваются по целым пикселям, а толщина рамки округляется до целого.
var snapSizes = map[int]bool{20: true, 24: true, 40: true}

// geometryFor возвращает фигуры значка заданного размера: исходные или с краями
// по сетке пикселей.
func geometryFor(size int) geometry {
	if !snapSizes[size] {
		return geometry{bigOuter, bigInner, knockout, smallScr}
	}
	k := float64(size) / 1024
	px := func(v float64) float64 { return math.Round(v * k) } // координата в пикселях
	back := func(p float64) float64 { return p / k }           // обратно в систему 1024
	snapRect := func(x0, y0, x1, y1, r float64) func(x, y float64) float64 {
		return rrect(back(px(x0)), back(px(y0)), back(px(x1)), back(px(y1)), r)
	}
	stroke := math.Max(1, px(64))
	ox0, oy0, ox1, oy1 := px(128), px(256), px(768), px(672)
	return geometry{
		bigOuter: rrect(back(ox0), back(oy0), back(ox1), back(oy1), 96),
		bigInner: rrect(back(ox0+stroke), back(oy0+stroke), back(ox1-stroke), back(oy1-stroke), 32),
		knockout: snapRect(480, 480, 960, 816, 96),
		smallScr: snapRect(512, 512, 896, 768, 64),
	}
}

// inFrame — точка (в системе 1024, y вниз) принадлежит рамке большого экрана.
func (g geometry) inFrame(x, y float64) bool {
	return g.bigOuter(x, y) < 0 && g.bigInner(x, y) >= 0 && g.knockout(x, y) >= 0
}

func (g geometry) inSmall(x, y float64) bool { return g.smallScr(x, y) < 0 }

// --- 16 пикселей (s1-16-sender.svg): целые координаты, без сглаживания ---

type pt struct{ x, y int }

// Контуры из s1-16.svg (до зеркалирования): рамка (четно-нечётное правило:
// внешний контур минус внутренний), малый экран и вырез x>=7, y>=7.
var (
	pxFrameOuter = []pt{{2, 2}, {10, 2}, {10, 3}, {11, 3}, {11, 8}, {10, 8}, {10, 9}, {2, 9}, {2, 8}, {1, 8}, {1, 3}, {2, 3}}
	pxFrameInner = []pt{{2, 3}, {2, 8}, {10, 8}, {10, 3}}
	pxSmall      = []pt{{9, 8}, {14, 8}, {14, 9}, {15, 9}, {15, 13}, {14, 13}, {14, 14}, {9, 14}, {9, 13}, {8, 13}, {8, 9}, {9, 9}}
)

// inPoly — точка внутри многоугольника (чётно-нечётное правило).
func inPoly(poly []pt, x, y float64) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		a, b := poly[i], poly[j]
		if (float64(a.y) > y) != (float64(b.y) > y) {
			xc := float64(a.x) + (y-float64(a.y))*float64(b.x-a.x)/float64(b.y-a.y)
			if x < xc {
				in = !in
			}
		}
	}
	return in
}

// mark — кого рисовать в точке: 0 — пусто, 1 — рамка, 2 — малый экран.
type mark uint8

const (
	markNone mark = iota
	markFrame
	markSmall
)

// markAt — что в точке (x,y) значка 1024 в варианте передатчика (зеркало по горизонтали).
func (g geometry) markAt(x, y float64) mark {
	x = 1024 - x
	if g.inSmall(x, y) {
		return markSmall
	}
	if g.inFrame(x, y) {
		return markFrame
	}
	return markNone
}

// markAt16 — что в пикселе (px,py) значка 16x16 в варианте передатчика.
func markAt16(px, py int) mark {
	x := float64(15-px) + 0.5 // зеркало: translate(16 0) scale(-1 1)
	y := float64(py) + 0.5
	if inPoly(pxSmall, x, y) {
		return markSmall
	}
	if inPoly(pxFrameOuter, x, y) && !inPoly(pxFrameInner, x, y) && !(x >= 7 && y >= 7) {
		return markFrame
	}
	return markNone
}

// palette задаёт цвета рамки и малого экрана и подложки (alpha 0 — прозрачная).
type palette struct {
	bg, frame, small color.NRGBA
}

var (
	paletteApp = palette{bg: colBG, frame: colMark, small: colAccent}
)

func monoPalette(c color.NRGBA, live bool) palette {
	p := palette{frame: c, small: c}
	if live {
		p.small = colAccent
	}
	return p
}

func blend(dst color.NRGBA, src color.NRGBA, cover float64) color.NRGBA {
	// Складываем в предумноженных цветах.
	sa := float64(src.A) / 255 * cover
	da := float64(dst.A) / 255
	oa := sa + da*(1-sa)
	if oa == 0 {
		return color.NRGBA{}
	}
	ch := func(s, d uint8) uint8 {
		v := (float64(s)*sa + float64(d)*da*(1-sa)) / oa
		return uint8(v + 0.5)
	}
	return color.NRGBA{ch(src.R, dst.R), ch(src.G, dst.G), ch(src.B, dst.B), uint8(oa*255 + 0.5)}
}

// render рисует значок size x size.
func render(size int, p palette) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	if size == 16 {
		for py := 0; py < 16; py++ {
			for px := 0; px < 16; px++ {
				c := p.bg
				switch markAt16(px, py) {
				case markFrame:
					c = p.frame
				case markSmall:
					c = p.small
				}
				img.SetNRGBA(px, py, c)
			}
		}
		return img
	}
	ss := 4
	if size <= 64 {
		ss = 8
	}
	n := float64(ss * ss)
	g := geometryFor(size)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var frame, small int
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/float64(ss)) * 1024 / float64(size)
					y := (float64(py) + (float64(sy)+0.5)/float64(ss)) * 1024 / float64(size)
					switch g.markAt(x, y) {
					case markFrame:
						frame++
					case markSmall:
						small++
					}
				}
			}
			c := p.bg
			if frame > 0 {
				c = blend(c, p.frame, float64(frame)/n)
			}
			if small > 0 {
				c = blend(c, p.small, float64(small)/n)
			}
			img.SetNRGBA(px, py, c)
		}
	}
	return img
}

// --- ICO ---

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// encodeDIB кодирует изображение как запись ICO в формате DIB: заголовок
// BITMAPINFOHEADER (высота вдвое больше: изображение и маска), пиксели BGRA
// снизу вверх и пустая AND-маска (прозрачность берётся из альфа-канала).
func encodeDIB(img *image.NRGBA) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	var out bytes.Buffer
	put := func(v any) { _ = binary.Write(&out, binary.LittleEndian, v) }
	put(uint32(40))
	put(int32(w))
	put(int32(h * 2))
	put(uint16(1))
	put(uint16(32))
	put(uint32(0))
	put(uint32(w * h * 4))
	put(int32(0))
	put(int32(0))
	put(uint32(0))
	put(uint32(0))
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			c := img.NRGBAAt(x, y)
			out.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	rowBytes := ((w + 31) / 32) * 4
	out.Write(make([]byte, rowBytes*h))
	return out.Bytes()
}

// writeICO собирает ICO: 256 px как PNG, остальные размеры как DIB.
func writeICO(path string, sizes []int, p palette) error {
	var images [][]byte
	for _, s := range sizes {
		img := render(s, p)
		if s >= 256 {
			images = append(images, encodePNG(img))
		} else {
			images = append(images, encodeDIB(img))
		}
	}
	var out bytes.Buffer
	w := func(v any) { _ = binary.Write(&out, binary.LittleEndian, v) }
	w(uint16(0))
	w(uint16(1))
	w(uint16(len(sizes)))
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		dim := uint8(s)
		if s >= 256 {
			dim = 0
		}
		w(dim)
		w(dim)
		w(uint8(0))
		w(uint8(0))
		w(uint16(1))
		w(uint16(32))
		w(uint32(len(images[i])))
		w(uint32(offset))
		offset += len(images[i])
	}
	for _, im := range images {
		out.Write(im)
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}

// preview собирает лист для проверки глазами: ряды значков 16, 20, 24, 32, 48
// пикселей, увеличенных в целое число раз без сглаживания, на тёмной и светлой подложке.
func preview() *image.NRGBA {
	type row struct {
		name string
		p    palette
		back color.NRGBA
	}
	rows := []row{
		{"app", paletteApp, color.NRGBA{0x30, 0x30, 0x30, 255}},
		{"tray-white", monoPalette(colWhite, false), color.NRGBA{0x20, 0x20, 0x20, 255}},
		{"tray-white-live", monoPalette(colWhite, true), color.NRGBA{0x20, 0x20, 0x20, 255}},
		{"tray-black", monoPalette(colBlack, false), color.NRGBA{0xF3, 0xF3, 0xF3, 255}},
		{"tray-black-live", monoPalette(colBlack, true), color.NRGBA{0xF3, 0xF3, 0xF3, 255}},
	}
	sizes := []int{16, 20, 24, 32, 48}
	const zoom = 4
	const pad = 16
	cell := 48*zoom + pad
	img := image.NewNRGBA(image.Rect(0, 0, pad+len(sizes)*cell/1, pad+len(rows)*cell))
	for ri, r := range rows {
		draw.Draw(img, image.Rect(0, pad/2+ri*cell, img.Bounds().Dx(), pad/2+(ri+1)*cell), image.NewUniform(r.back), image.Point{}, draw.Src)
		for si, s := range sizes {
			ic := render(s, r.p)
			ox, oy := pad+si*cell, pad+ri*cell
			for y := 0; y < s*zoom; y++ {
				for x := 0; x < s*zoom; x++ {
					c := ic.NRGBAAt(x/zoom, y/zoom)
					dst := img.NRGBAAt(ox+x, oy+y)
					img.SetNRGBA(ox+x, oy+y, blend(dst, c, 1))
				}
			}
		}
	}
	return img
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func main() {
	dir := "assets"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fatal(err)
	}
	outputs := []struct {
		name  string
		sizes []int
		p     palette
	}{
		{"airspoon", appSizes, paletteApp},
		{"tray-white", traySizes, monoPalette(colWhite, false)},
		{"tray-black", traySizes, monoPalette(colBlack, false)},
		{"tray-white-live", traySizes, monoPalette(colWhite, true)},
		{"tray-black-live", traySizes, monoPalette(colBlack, true)},
	}
	for _, o := range outputs {
		path := filepath.Join(dir, o.name+".ico")
		if err := writeICO(path, o.sizes, o.p); err != nil {
			fatal(err)
		}
		fmt.Println("записан", path)
	}
	if err := os.WriteFile(filepath.Join(dir, "airspoon.png"), encodePNG(render(256, paletteApp)), 0o644); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "preview.png"), encodePNG(preview()), 0o644); err != nil {
		fatal(err)
	}
}
