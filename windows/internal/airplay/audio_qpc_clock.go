package airplay

import "time"

// qpcClockSources — источники времени для qpcClock. В Windows это
// QueryPerformanceCounter, QueryInterruptTimePrecise и time.Now; тесты
// подменяют их виртуальными часами с грубым тиком.
type qpcClockSources struct {
	// qpc возвращает QPC в единицах 100 нс.
	qpc func() uint64
	// precise возвращает точное время прерываний (QueryInterruptTimePrecise,
	// тот же ход времени, что у монотонного отсчёта time.Now, но без грубого
	// тика). ok=false, если вызов недоступен.
	precise func() (d time.Duration, ok bool)
	// now — time.Now: в Windows его монотонный отсчёт обновляется только
	// на тике таймера (до 15,6 мс).
	now func() time.Time
}

// qpcClockSpinLimit ограничивает ожидание границы тика при начальной привязке.
const qpcClockSpinLimit = 50 * time.Millisecond

// qpcClock переводит позиции QPC из WASAPI (единицы 100 нс) в time.Time в том
// же монотонном домене, что и метки видеокадров (time.Now).
//
// Раньше якорь брался из time.Now() раз в секунду. Монотонный отсчёт time.Now
// в Windows обновляется только на тике таймера (15,6 мс), поэтому каждый новый
// якорь «прыгал» на ±15 мс, и PTS пакетов вместе с ним. Теперь time.Now
// читается ровно один раз — в момент смены тика, когда его значение совпадает
// с истинным временем с точностью до микросекунд; дальше домен time.Now
// продолжается точными часами (precise): якорь = (QPC, base + прирост precise).
type qpcClock struct {
	src        qpcClockSources
	anchorQPC  uint64
	anchorTime time.Time

	// Привязка домена time.Now к точным часам: baseTime ↔ basePrecise.
	havePrecise bool
	baseTime    time.Time
	basePrecise time.Duration
	bestSpread  time.Duration
}

func newQPCClockFrom(src qpcClockSources) *qpcClock {
	c := &qpcClock{src: src}
	if _, ok := src.precise(); ok {
		c.syncToTick()
	}
	c.reanchor()
	return c
}

// syncToTick ждёт смены монотонного отсчёта time.Now (граница тика) и
// сразу читает точные часы: в этот момент грубое значение не отстаёт от
// истинного времени.
func (c *qpcClock) syncToTick() {
	first := c.src.now()
	t := first
	startQPC := c.src.qpc()
	limit := uint64(qpcClockSpinLimit / 100)
	for t.Sub(first) == 0 && c.src.qpc()-startQPC < limit {
		t = c.src.now()
	}
	p, ok := c.src.precise()
	if !ok {
		return
	}
	c.havePrecise = true
	c.baseTime = t
	c.basePrecise = p
}

// reanchor обновляет пару (QPC, time.Time). С точными часами time.Now не
// читается вовсе, поэтому якорь не прыгает на тик; без них — прежний способ
// (самое тесное из трёх чтений).
func (c *qpcClock) reanchor() {
	if c.havePrecise {
		for i := 0; i < 3; i++ {
			p0, _ := c.src.precise()
			qpc := c.src.qpc()
			p1, _ := c.src.precise()
			if i == 0 || p1-p0 < c.bestSpread {
				c.bestSpread = p1 - p0
				c.anchorQPC = qpc
				c.anchorTime = c.baseTime.Add((p0 + (p1-p0)/2) - c.basePrecise)
			}
		}
		return
	}
	best := time.Duration(1<<63 - 1)
	for i := 0; i < 3; i++ {
		before := c.src.now()
		qpc := c.src.qpc()
		after := c.src.now()
		if spread := after.Sub(before); spread < best {
			best = spread
			c.anchorQPC = qpc
			c.anchorTime = before.Add(spread / 2)
		}
	}
}

func (c *qpcClock) timeAt(qpc100ns uint64) time.Time {
	return c.anchorTime.Add(time.Duration(int64(qpc100ns-c.anchorQPC)) * 100)
}

// nowTime — текущее время в домене time.Now, но с точностью до микросекунд.
func (c *qpcClock) nowTime() time.Time {
	return c.timeAt(c.src.qpc())
}
