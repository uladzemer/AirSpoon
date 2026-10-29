package airplay

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// Keep timeout tests quick while checking that production sets both deadlines.
type shortDeadlineConn struct {
	net.Conn
	mu       sync.Mutex
	deadline time.Time
}

func (c *shortDeadlineConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	if !deadline.IsZero() {
		c.deadline = deadline
	}
	c.mu.Unlock()
	if !deadline.IsZero() {
		deadline = time.Now().Add(50 * time.Millisecond)
	}
	return c.Conn.SetDeadline(deadline)
}

func (c *shortDeadlineConn) SetReadDeadline(deadline time.Time) error {
	if !deadline.IsZero() {
		deadline = time.Now().Add(50 * time.Millisecond)
	}
	return c.Conn.SetReadDeadline(deadline)
}

func TestControlRequestsBoundWritesAndReads(t *testing.T) {
	for _, api := range []string{"http", "raw", "rtsp"} {
		for _, phase := range []string{"write", "read"} {
			t.Run(api+"/"+phase, func(t *testing.T) {
				local, remote := net.Pipe()
				defer local.Close()
				defer remote.Close()
				conn := &shortDeadlineConn{Conn: local}
				client := &AirPlayClient{conn: conn}
				if phase == "read" {
					go io.Copy(io.Discard, remote)
				}
				started := time.Now()
				var err error
				switch api {
				case "http":
					_, err = client.httpRequest("TEARDOWN", "/stream", "", nil)
				case "raw":
					_, err = client.rawRequest("TEARDOWN", "/stream", "", nil)
				case "rtsp":
					_, _, err = client.rtspRequest("TEARDOWN", "/stream", "", nil, nil)
				}
				var timeout net.Error
				if !errors.As(err, &timeout) || !timeout.Timeout() || !hasIncompleteResponse(err) {
					t.Fatalf("request error = %v, want transport timeout", err)
				}
				if time.Since(started) > time.Second {
					t.Fatal("request remained blocked")
				}
				if conn.deadline.IsZero() || conn.deadline.Sub(started) > 2100*time.Millisecond {
					t.Fatalf("TEARDOWN deadline = %v", conn.deadline.Sub(started))
				}
			})
		}
	}
}

func TestHTTPRequestDeadlineIncludesWriteTime(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	client := &AirPlayClient{conn: local}
	done := make(chan error, 1)
	go func() {
		// Consume most of the request budget while the client is writing.
		time.Sleep(80 * time.Millisecond)
		_, err := readRTSPTestRequest(bufio.NewReader(remote))
		if err == nil {
			time.Sleep(80 * time.Millisecond)
			err = writeRTSPTestResponse(remote, 200, nil, nil)
		}
		done <- err
	}()
	started := time.Now()
	_, _, err := client.httpRequestWithHeadersTimeout("POST", "/feedback", "", nil, 120*time.Millisecond)
	if err == nil {
		t.Fatal("read got a new timeout budget after the write")
	}
	if time.Since(started) > 190*time.Millisecond {
		t.Fatal("request exceeded its overall deadline")
	}
	local.Close()
	<-done
}

func TestFeedbackTransportFailureFailsStream(t *testing.T) {
	for _, status := range []int{0, 500} {
		local, remote := net.Pipe()
		ctx, cancel := context.WithCancel(context.Background())
		session := &MirrorSession{client: &AirPlayClient{conn: local}, streamDone: make(chan struct{})}
		done := make(chan struct{})
		go func() { session.feedbackLoop(ctx); close(done) }()
		if _, err := readRTSPTestRequest(bufio.NewReader(remote)); err != nil {
			t.Fatal(err)
		}
		if status == 0 {
			remote.Close()
			select {
			case <-session.streamDone:
				if err := session.streamFailure(); err == nil || !strings.Contains(err.Error(), "feedback control channel") {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("feedback disconnect did not fail stream")
			}
		} else {
			if err := writeRTSPTestResponse(remote, status, nil, nil); err != nil {
				t.Fatal(err)
			}
			if session.streamFailure() != nil {
				t.Fatal("complete HTTP rejection treated as broken transport")
			}
		}
		cancel()
		local.Close()
		remote.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("feedback worker did not stop")
		}
	}
}

func TestMirrorCloseBoundsBlockedFeedbackAndClosesWorkers(t *testing.T) {
	for _, phase := range []string{"write", "read"} {
		t.Run(phase, func(t *testing.T) {
			local, remote := net.Pipe()
			defer remote.Close()
			data, peer := net.Pipe()
			defer peer.Close()
			ctx, cancel := context.WithCancel(context.Background())
			session := &MirrorSession{client: &AirPlayClient{conn: local}, sessionURI: "/stream", dataConn: data, cancel: cancel, streamDone: make(chan struct{})}
			session.startWorker(func() { session.monitorDataConnection(data) })
			entered := make(chan struct{})
			session.startWorker(func() { close(entered); session.feedbackLoop(ctx) })
			<-entered
			if phase == "read" {
				if _, err := readRTSPTestRequest(bufio.NewReader(remote)); err != nil {
					t.Fatal(err)
				}
			}
			started := time.Now()
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			if time.Since(started) > 3*time.Second {
				t.Fatal("Close waited for normal feedback timeout")
			}
			if err := session.Close(); err != nil {
				t.Fatal("second Close:", err)
			}
		})
	}
}
