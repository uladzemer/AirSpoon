package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"airspoon/internal/airplay"
	"airspoon/internal/ffmpegfind"
)

const probeTimeout = 15 * time.Second // холодный первый запуск ffmpeg.exe (антивирус) бывает медленным

// Source — запущенный ffmpeg.exe; реализует airplay.VideoSource.
type Source struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	frames chan airplay.VideoAccessUnit
	done   chan struct{} // закрывается после завершения читающей горутины

	mu      sync.Mutex
	readErr error // причина завершения потока (nil = чистый EOF)

	stderr *tailBuffer
	once   sync.Once
}

var _ airplay.VideoSource = (*Source)(nil)

// FindFFmpeg ищет ffmpeg.exe (см. ffmpegfind.Find).
func FindFFmpeg(explicit string) (string, error) { return ffmpegfind.Find(explicit) }

// Start запускает ffmpeg. Процесс завершается при отмене ctx, при Close и
// вместе с нашим процессом (Job Object).
func Start(ctx context.Context, cfg Config) (*Source, error) {
	args, err := BuildArgs(cfg)
	if err != nil {
		return nil, err
	}
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	path, err := FindFFmpeg(cfg.FFmpegPath)
	if err != nil {
		return nil, err
	}
	if err := probeDDAGrab(ctx, path); err != nil {
		return nil, err
	}

	return launch(ctx, path, args, cfg.Debugf, logf)
}

// launch запускает уже найденный процесс с готовыми аргументами и разбирает его
// вывод как Annex-B. Отдельная функция — чтобы тестировать разбор завершения
// процесса без настоящего ffmpeg.
func launch(ctx context.Context, path string, args []string, debugf, logf func(string, ...any)) (*Source, error) {
	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, path, args...)
	hideConsoleWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdout ffmpeg: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stderr ffmpeg: %w", err)
	}
	if debugf != nil {
		debugf("[FFMPEG] %s %s", path, strings.Join(args, " "))
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("запуск ffmpeg: %w", err)
	}
	if err := assignToJob(cmd.Process); err != nil {
		// Без Job Object остаётся Kill при Close/отмене контекста.
		logf("[FFMPEG] предупреждение: не удалось привязать ffmpeg к Job Object: %v", err)
	}

	s := &Source{
		cmd:    cmd,
		cancel: cancel,
		// Очередь короткая намеренно: пока приёмник ещё не принимает кадры (SETUP),
		// накопленные кадры стареют, а их PTS — время получения. Две единицы доступа
		// ограничивают этот хвост (~66 мс при 30 к/с); остальное задерживает сам
		// ffmpeg через заполнение канала stdout.
		frames: make(chan airplay.VideoAccessUnit, 2),
		done:   make(chan struct{}),
		stderr: &tailBuffer{max: 4096},
	}
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			s.stderr.add(line)
			logf("[FFMPEG] %s", line)
		}
	}()
	go s.readLoop(stdout, stderrDone)
	return s, nil
}

// probeDDAGrab проверяет, что сборка ffmpeg умеет захват через Desktop
// Duplication, чтобы дать понятную ошибку до запуска.
func probeDDAGrab(ctx context.Context, ffmpeg string) error {
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	probe := exec.CommandContext(pctx, ffmpeg, "-hide_banner", "-h", "filter=ddagrab")
	hideConsoleWindow(probe)
	var out bytes.Buffer
	probe.Stdout = &out
	probe.Stderr = &out
	if err := probe.Run(); err != nil {
		if pctx.Err() != nil {
			return fmt.Errorf("проверка ddagrab в ffmpeg: %w", pctx.Err())
		}
		return fmt.Errorf("этот ffmpeg не содержит фильтра ddagrab (нужна сборка для Windows с Desktop Duplication): %w; вывод: %s",
			err, strings.TrimSpace(out.String()))
	}
	if !strings.Contains(out.String(), "Desktop Duplication") {
		return errors.New("фильтр ddagrab недоступен в этой сборке ffmpeg")
	}
	return nil
}

func (s *Source) readLoop(stdout io.Reader, stderrDone <-chan struct{}) {
	defer close(s.done)
	defer close(s.frames)
	var asm assembler
	buf := make([]byte, 64*1024)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			for _, au := range asm.push(buf[:n], time.Now()) {
				// Сначала отдаём кадр; блокировка здесь означает, что отправитель
				// отстаёт, и ffmpeg притормозится через заполнение канала stdout.
				s.frames <- au
			}
		}
		if err != nil {
			for _, au := range asm.finish() {
				s.frames <- au
			}
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
				s.setErr(err)
			}
			break
		}
	}
	// Сначала дочитываем stderr до конца: Wait закрывает чтение канала, и всё,
	// что горутина не успела прочитать, пропало бы из текста ошибки (так требует
	// документация os/exec). Затем забираем код возврата, чтобы не оставить zombie
	// и объяснить ошибку.
	<-stderrDone
	waitErr := s.cmd.Wait()
	if waitErr != nil {
		s.mu.Lock()
		if s.readErr == nil {
			s.readErr = fmt.Errorf("ffmpeg завершился: %w", waitErr)
			if tail := s.stderr.String(); tail != "" {
				s.readErr = fmt.Errorf("%w; stderr: %s", s.readErr, tail)
			}
		}
		s.mu.Unlock()
	}
}

func (s *Source) setErr(err error) {
	s.mu.Lock()
	if s.readErr == nil {
		s.readErr = err
	}
	s.mu.Unlock()
}

// ReadAccessUnit возвращает следующий кадр. После завершения ffmpeg — io.EOF
// (штатно) или ошибка с хвостом stderr.
func (s *Source) ReadAccessUnit(ctx context.Context) (airplay.VideoAccessUnit, error) {
	select {
	case au, ok := <-s.frames:
		if ok {
			return au, nil
		}
	case <-ctx.Done():
		return airplay.VideoAccessUnit{}, ctx.Err()
	}
	<-s.done
	s.mu.Lock()
	err := s.readErr
	s.mu.Unlock()
	if err == nil {
		if tail := s.stderr.String(); tail != "" {
			return airplay.VideoAccessUnit{}, fmt.Errorf("%w; stderr ffmpeg: %s", io.EOF, tail)
		}
		return airplay.VideoAccessUnit{}, io.EOF
	}
	return airplay.VideoAccessUnit{}, err
}

// Close останавливает ffmpeg и освобождает ресурсы; повторный вызов безопасен.
func (s *Source) Close() error {
	s.once.Do(func() {
		s.cancel()
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		// Освобождаем читателя, если отправитель не забирает кадры.
		go func() {
			for range s.frames {
			}
		}()
		select {
		case <-s.done:
		case <-time.After(3 * time.Second):
		}
	})
	return nil
}

// tailBuffer хранит последние строки stderr для сообщений об ошибках.
type tailBuffer struct {
	mu    sync.Mutex
	max   int
	lines []string
	size  int
}

func (t *tailBuffer) add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	t.size += len(line) + 1
	for t.size > t.max && len(t.lines) > 1 {
		t.size -= len(t.lines[0]) + 1
		t.lines = t.lines[1:]
	}
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, " | ")
}
