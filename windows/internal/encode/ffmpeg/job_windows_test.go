//go:build windows

package ffmpeg

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// processAlive сообщает, работает ли процесс с данным pid.
func processAlive(pid uint32) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}

// Вспомогательный «родитель»: запускает ffmpeg, привязывает его к Job Object,
// печатает pid и ждёт, пока внешний тест не убьёт его.
func TestJobHelperProcess(t *testing.T) {
	if os.Getenv("AIRSPOON_JOB_HELPER") != "1" {
		t.Skip("вспомогательный процесс")
	}
	c := exec.Command(os.Getenv("AIRSPOON_JOB_FFMPEG"), "-hide_banner", "-loglevel", "quiet",
		"-re", "-f", "lavfi", "-i", "testsrc=size=64x64:rate=5", "-f", "null", "-")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if err := assignToJob(c.Process); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("PID %d\n", c.Process.Pid)
	time.Sleep(2 * time.Minute)
}

func TestJobKillsFFmpegWhenParentDies(t *testing.T) {
	path, err := FindFFmpeg(os.Getenv("AIRSPOON_FFMPEG"))
	if err != nil {
		t.Skip("ffmpeg.exe не найден:", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	parent := exec.Command(exe, "-test.run=^TestJobHelperProcess$")
	parent.Env = append(os.Environ(), "AIRSPOON_JOB_HELPER=1", "AIRSPOON_JOB_FFMPEG="+path)
	out, err := parent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	defer parent.Process.Kill()

	var childPID uint32
	sc := bufio.NewScanner(out)
	deadline := time.After(20 * time.Second)
	found := make(chan struct{})
	go func() {
		for sc.Scan() {
			if rest, ok := strings.CutPrefix(sc.Text(), "PID "); ok {
				n, _ := strconv.Atoi(rest)
				childPID = uint32(n)
				close(found)
				return
			}
		}
	}()
	select {
	case <-found:
	case <-deadline:
		t.Fatal("вспомогательный процесс не сообщил pid ffmpeg")
	}
	if !processAlive(childPID) {
		t.Fatalf("ffmpeg (pid %d) не запущен", childPID)
	}

	// Аварийно завершаем «родителя»: закрытие дескриптора Job Object должно
	// убить ffmpeg.
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = parent.Process.Wait()
	for i := 0; i < 50; i++ {
		if !processAlive(childPID) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("ffmpeg (pid %d) пережил родительский процесс", childPID)
}
