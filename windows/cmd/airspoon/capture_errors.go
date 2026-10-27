package main

import (
	"errors"

	"airspoon/internal/capture/dxgi"
	"airspoon/internal/encode/mf"
	"airspoon/internal/trayctl"
)

// classifyCaptureError сопоставляет ошибки захвата и кодера с признаками trayctl,
// по которым пользователь получает понятный текст. Возвращает nil, если ошибка
// не из этой группы. Проверяется вся цепочка: те же ошибки приходят и при
// запуске показа, и из уже идущего (HDR включили, монитор отключили).
func classifyCaptureError(err error) error {
	var unsupported *dxgi.UnsupportedError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, mf.ErrUnavailable):
		return trayctl.ErrNoMediaFoundation
	case errors.Is(err, dxgi.ErrFrameFormat):
		return trayctl.ErrCaptureHDR
	case errors.As(err, &unsupported) && !unsupported.Hybrid:
		// Одна видеокарта: сменить её нельзя, причина в сеансе (RDP, виртуалка).
		return trayctl.ErrCaptureSession
	case errors.Is(err, dxgi.ErrCaptureStale):
		return trayctl.ErrCaptureStopped
	}
	return nil
}

// isFatalCaptureError — ошибка захвата или кодера, которую перезапуск источника
// не лечит (включили HDR, нет Media Foundation): Supervise не должен 2 минуты
// держать замёрзший кадр на телевизоре, а сразу вернуть ошибку.
func isFatalCaptureError(err error) bool {
	return errors.Is(err, dxgi.ErrFrameFormat) || errors.Is(err, mf.ErrUnavailable)
}

// cliCaptureAdvice объясняет человеку в консоли, почему не работает захват или
// кодирование. Для гибридных ноутбуков (DXGI_ERROR_UNSUPPORTED) при необходимости
// записывает предпочтение видеокарты.
func cliCaptureAdvice(err error, out func(string)) {
	if kind := classifyCaptureError(err); kind != nil {
		out(trayctl.UserMessage(kind, ""))
		return
	}
	if errors.Is(err, dxgi.ErrDuplicateUnsupported) {
		cliUnsupportedAdvice(newGPUEnv(), err, out)
	}
}

// warnIgnoredEncoderFlags предупреждает о флагах, которые выбранный кодер не
// учитывает: --no-gpu-convert относится только к нативному пути, при
// --encoder=ffmpeg цвет преобразует сам ffmpeg.
func warnIgnoredEncoderFlags(o mirrorOptions, logf func(string, ...any)) {
	if useFFmpeg, _, err := parseEncoder(o.encoder); err == nil && useFFmpeg && o.noGPUConvert {
		logf("предупреждение: --no-gpu-convert не действует при --encoder=ffmpeg (цвет преобразует сам ffmpeg); флаг игнорируется")
	}
}

// audioLostNotice — текст для трея, когда передача звука оборвалась насовсем.
const audioLostNotice = "Звук не передаётся: не удалось получить звук компьютера. Экран показывается без звука. Чтобы вернуть звук, выберите телевизор в меню ещё раз."

// notifyAudioLost сообщает пользователю в трее, что звук пропал до конца показа.
// notice == nil (консольный режим) — только строка в журнале, она уже записана.
func notifyAudioLost(notice func(string)) {
	if notice != nil {
		notice(audioLostNotice)
	}
}
