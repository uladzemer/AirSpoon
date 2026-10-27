package mf

import "testing"

func TestChooseH264Level(t *testing.T) {
	cases := []struct {
		w, h, fps int
		want      int
	}{
		{1280, 720, 30, 41},  // 108000 МБ/с: ниже 4.1 не опускаемся
		{1280, 720, 60, 41},  // 216000
		{1920, 1080, 30, 41}, // 244800 <= 245760, кадр 8160 <= 8192
		{1920, 1080, 60, 42}, // 489600 > 245760, но <= 522240; кадр 8160 <= 8704
		{1920, 1080, 120, 51},
		{2560, 1440, 30, 51}, // кадр 14400 > 8704
		{2560, 1440, 60, 51}, // 864000 <= 983040
		{3440, 1440, 60, 52}, // 215*90=19350 МБ (3440/16 округляется вверх), 1161000 > 983040
		{3840, 2160, 30, 51}, // 972000 <= 983040
		{3840, 2160, 60, 52},
		{7680, 4320, 60, 52}, // больше 5.2: потолок
	}
	for _, c := range cases {
		got := chooseH264Level(c.w, c.h, c.fps)
		if got != c.want {
			t.Errorf("%dx%d@%d: уровень %d, ожидали %d", c.w, c.h, c.fps, got, c.want)
		}
	}
}

func TestMacroblocksPerSecondRoundsUp(t *testing.T) {
	// 1080 не делится на 16: 68 строк макроблоков, как и у кодера.
	pf, ps := macroblocksPerSecond(1920, 1080, 60)
	if pf != 120*68 || ps != 120*68*60 {
		t.Fatalf("получили %d и %d", pf, ps)
	}
}

func TestLevelAndProfileNames(t *testing.T) {
	if h264LevelName(42) != "4.2" || h264LevelName(51) != "5.1" {
		t.Fatal("названия уровней")
	}
	if h264ProfileName(77) != "Main" || h264ProfileName(100) != "High" || h264ProfileName(66) != "Baseline" {
		t.Fatal("названия профилей")
	}
}
