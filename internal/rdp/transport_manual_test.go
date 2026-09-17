package rdp

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestManualDialAgainstXrdp is a throwaway, manually-invoked check against
// the real xrdp test server running in WSL — not part of the normal test
// suite (skipped unless RDP_MANUAL_TEST_ADDR is set). Delete once the RDP
// client is fully built and covered by real unit tests + app-level
// verification, matching how internal/rfb was verified.
func TestManualDialAgainstXrdp(t *testing.T) {
	addr := os.Getenv("RDP_MANUAL_TEST_ADDR")
	if addr == "" {
		t.Skip("RDP_MANUAL_TEST_ADDR not set")
	}
	c, err := Dial(context.Background(), addr, DialOptions{Username: "rdptest", Password: "lupinus1"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Logf("dial succeeded: mcsUserID=%d ioChannelID=%d serverWidth=%d serverHeight=%d",
		c.mcsUserID, c.ioChannelID, c.serverWidth, c.serverHeight)

	sink := &logSink{t: t}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	err = c.Run(ctx, sink)
	t.Logf("Run returned: %v (updates=%d)", err, sink.updates)
	if sink.updates == 0 {
		t.Errorf("no framebuffer updates received in 6s")
	}
}

type logSink struct {
	t       *testing.T
	updates int
}

func (s *logSink) Init(width, height int, name string) {
	s.t.Logf("Init: %dx%d name=%q", width, height, name)
}

func (s *logSink) Update(x, y, w, h int, rgba []byte) {
	s.updates++
	if s.updates <= 5 || s.updates%50 == 0 {
		sample := ""
		if len(rgba) >= 8 {
			sample = fmt.Sprintf("% x", rgba[:8])
		}
		s.t.Logf("Update #%d: (%d,%d) %dx%d bytes=%d first-pixels=%s", s.updates, x, y, w, h, len(rgba), sample)
	}
}

func (s *logSink) Resize(width, height int) {
	s.t.Logf("Resize: %dx%d", width, height)
}

func (s *logSink) Cursor(hotX, hotY, w, h int, rgba []byte) {}

func (s *logSink) CutText(text string) {
	s.t.Logf("CutText: %q", text)
}
