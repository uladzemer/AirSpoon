// Пакет applog — журнал программы в файле с ротацией: когда файл вырастает
// больше лимита, он переименовывается в «.1» (прежний «.1» заменяется) и
// запись идёт в новый файл. Так журнал занимает не больше двух лимитов.
package applog

import (
	"os"
	"path/filepath"
	"sync"
)

// DefaultMaxBytes — лимит одного файла журнала (около 5 МБ).
const DefaultMaxBytes = 5 << 20

// Rotator — io.WriteCloser с ротацией, безопасный для параллельной записи.
type Rotator struct {
	path string
	max  int64

	mu   sync.Mutex
	f    *os.File
	size int64
}

// Open открывает (создаёт) журнал path; при max <= 0 берётся DefaultMaxBytes.
func Open(path string, max int64) (*Rotator, error) {
	if max <= 0 {
		max = DefaultMaxBytes
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	r := &Rotator{path: path, max: max}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Rotator) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

// Write пишет в журнал, при необходимости сначала выполняя ротацию.
func (r *Rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return 0, os.ErrClosed
	}
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		r.rotate()
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate: журнал не должен останавливать программу, поэтому ошибки
// переименования не фатальны (тогда файл просто продолжает расти).
func (r *Rotator) rotate() {
	_ = r.f.Close()
	r.f = nil
	_ = os.Remove(r.path + ".1")
	if err := os.Rename(r.path, r.path+".1"); err != nil {
		_ = os.Truncate(r.path, 0)
	}
	if err := r.open(); err != nil {
		// Последняя попытка: открыть без добавления, чтобы Write не падал.
		if f, e2 := os.OpenFile(os.DevNull, os.O_WRONLY, 0); e2 == nil {
			r.f, r.size = f, 0
		}
	}
}

// Close закрывает журнал.
func (r *Rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// DefaultPath — %LOCALAPPDATA%\AirSpoon\sender.log.
func DefaultPath() string {
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		if d, err := os.UserCacheDir(); err == nil {
			dir = d
		} else {
			dir = os.TempDir()
		}
	}
	return filepath.Join(dir, "AirSpoon", "sender.log")
}
