package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"airspoon/internal/airplay"
	"airspoon/internal/encode/mf"
	"airspoon/internal/encode/native"
)

type failingVideoSource struct {
	reads  int
	closes atomic.Int32
}

func (s *failingVideoSource) ReadAccessUnit(context.Context) (airplay.VideoAccessUnit, error) {
	s.reads++
	if s.reads == 1 {
		return airplay.VideoAccessUnit{AnnexB: annexB(testSPS, testPPS, testIDR), PTS: time.Now()}, nil
	}
	return airplay.VideoAccessUnit{}, mf.ErrUnavailable
}

func (s *failingVideoSource) Close() error { s.closes.Add(1); return nil }

func TestMirrorFailureCancelsAttemptAndClosesSourceOnce(t *testing.T) {
	quietDiagnostics(t)
	server := startTestReceiver(t, airplay.ReceiverConfig{Profile: airplay.ReceiverProfileUxPlay})
	old := startNativeSource
	defer func() { startNativeSource = old }()
	var attempt context.Context
	src := &failingVideoSource{}
	startNativeSource = func(ctx context.Context, _ native.Config) (airplay.VideoSource, error) {
		attempt = ctx
		return src, nil
	}
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := mirror(parent, mirrorOptions{host: server.Addr().String(), compat: "uxplay", noAudio: true,
		credFile: filepath.Join(t.TempDir(), "credentials.json")})
	if !errors.Is(err, mf.ErrUnavailable) {
		t.Fatalf("unexpected stream error: %v", err)
	}
	if parent.Err() != nil {
		t.Fatal("parent must remain active")
	}
	if attempt == nil || attempt.Err() == nil {
		t.Fatal("attempt context leaked")
	}
	if n := src.closes.Load(); n != 1 {
		t.Fatalf("source closed %d times", n)
	}
}

func TestMirrorUsesPasswordPrompt(t *testing.T) {
	for _, profile := range []airplay.ReceiverProfile{airplay.ReceiverProfileRoku, airplay.ReceiverProfileModern} {
		t.Run(string(profile), func(t *testing.T) {
			withCapturingVideo(t)
			const password = " living room! "
			server := startTestReceiver(t, airplay.ReceiverConfig{Profile: profile,
				Auth: airplay.ReceiverAuthDigest, Code: password, HidePasswordStatus: true})
			asked := false
			err := runAuto(t, server, filepath.Join(t.TempDir(), "credentials.json"), func(o *mirrorOptions) {
				o.pinPrompt = func(context.Context, bool) (string, error) { return "", errors.New("PIN dialog used for password") }
				o.passwordPrompt = func(context.Context, bool) (string, error) { asked = true; return password, nil }
			})
			if err != nil {
				t.Fatal(err)
			}
			if !asked {
				t.Fatal("password dialog was not requested")
			}
			requireVideoDelivered(t, server, profile)
		})
	}
}
