//go:build windows

package ffmpeg

import (
	"os/exec"
	"syscall"
)

// hideConsoleWindow не даёт ffmpeg.exe открывать своё консольное окно, когда
// airspoon запущен без консоли (в будущем — как GUI-приложение).
func hideConsoleWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
