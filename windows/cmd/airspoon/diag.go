package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"airspoon/internal/gpudiag"
)

// runDiag — команда «AirSpoonSender.exe diag»: печатает диагностику машины
// (система, Media Foundation, видеокарты и их драйверы, мониторы и захват,
// кодеры H.264 с пробным кодированием, сеть и звук) и итоговую рекомендацию
// одной строкой. Полный прогон занимает около минуты; --quick даёт короткую
// версию (без пробного кодирования и замера захвата). Тот же отчёт дописывается в
// sender.log. Код выхода: 0 — дублирование открылось (или проба отключена),
// 1 — не открылось.
func runDiag(args []string) int {
	fs := flag.NewFlagSet("diag", flag.ContinueOnError)
	probe := fs.Duration("probe", time.Second, "сколько пробовать открыть дублирование экрана (0 — не пробовать)")
	display := fs.Int("display", 0, "номер монитора для пробы")
	out := fs.String("out", "", "дополнительно записать отчёт в этот файл")
	quick := fs.Bool("quick", false, "короткая диагностика: без пробного кодирования, замера захвата, сети и звука")
	encodeProbe := fs.Duration("encode-probe", 2*time.Second, "длительность каждой пробы кодирования (0 — пропустить пробное кодирование)")
	measure := fs.Duration("measure", 5*time.Second, "сколько измерять захват каждого монитора (0 — не измерять)")
	host := fs.String("host", "", "адрес приёмника host[:порт] для определения типа сети (по умолчанию — последний телевизор)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	opts := gpudiag.Options{Probe: *probe, Display: *display, RetryEnv: os.Getenv(envGPURetry)}
	if !*quick {
		opts.Extended = true
		opts.Peer = *host
		if opts.Peer == "" {
			opts.Peer = lastReceiverHost()
		}
		// В Options ноль означает «по умолчанию», меньше нуля — «пропустить».
		opts.EncodeProbe, opts.MeasureCapture = *encodeProbe, *measure
		if *encodeProbe <= 0 {
			opts.EncodeProbe = -1
		}
		if *measure <= 0 {
			opts.MeasureCapture = -1
		}
		fmt.Fprintln(os.Stderr, "Диагностика займёт около минуты (--quick — короткая версия). Не закрывайте окно.")
		opts.Progress = func(s string) { fmt.Fprintln(os.Stderr, "  проверяю: "+s) }
	}
	rep := gpudiag.Collect(opts)
	lines := rep.Lines()
	for _, l := range lines {
		fmt.Println(l)
		appendLog("diag: %s", l)
	}
	if *out != "" {
		text := ""
		for _, l := range lines {
			text += l + "\r\n"
		}
		if err := os.WriteFile(*out, []byte(text), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "отчёт не записан в %s: %v\n", *out, err)
		}
	}
	if rep.Probe != nil && !rep.Probe.OK {
		return 1
	}
	return 0
}
