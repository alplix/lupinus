package rfb

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestManualTightAgainstTigerVNC is a throwaway, manually-invoked check
// against a real TigerVNC server (gated by RFB_MANUAL_TEST_ADDR, matching
// internal/rdp's pattern) — verifies the new Tight decoder against real
// wire data rather than only hand-built fixtures. Delete once Tight has
// been exercised against a couple of real servers over time.
func TestManualTightAgainstTigerVNC(t *testing.T) {
	addr := os.Getenv("RFB_MANUAL_TEST_ADDR")
	if addr == "" {
		t.Skip("RFB_MANUAL_TEST_ADDR not set")
	}
	password := os.Getenv("RFB_MANUAL_TEST_PASSWORD")

	c, err := Dial(context.Background(), addr, DialOptions{Password: password, DialTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Logf("dial succeeded: %dx%d %q", c.width, c.height, c.name)

	sink := &tightLogSink{t: t}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	err = c.Run(ctx, sink)
	t.Logf("Run returned: %v (updates=%d totalBytes=%d)", err, sink.updates, sink.totalBytes)
	if sink.updates == 0 {
		t.Errorf("no framebuffer updates received in 6s")
	}
}

type tightLogSink struct {
	t          *testing.T
	updates    int
	totalBytes int
}

func (s *tightLogSink) Init(width, height int, name string) {
	s.t.Logf("Init: %dx%d name=%q", width, height, name)
}

func (s *tightLogSink) Update(x, y, w, h int, rgba []byte) {
	s.updates++
	s.totalBytes += len(rgba)
	if s.updates <= 8 || s.updates%100 == 0 {
		sample := ""
		if len(rgba) >= 8 {
			sample = fmt.Sprintf("% x", rgba[:8])
		}
		s.t.Logf("Update #%d: (%d,%d) %dx%d bytes=%d first-pixels=%s", s.updates, x, y, w, h, len(rgba), sample)
	}
}

func (s *tightLogSink) CopyRect(dstX, dstY, w, h, srcX, srcY int) {
	s.t.Logf("CopyRect: (%d,%d) %dx%d <- (%d,%d)", dstX, dstY, w, h, srcX, srcY)
}

func (s *tightLogSink) Resize(width, height int) {
	s.t.Logf("Resize: %dx%d", width, height)
}

func (s *tightLogSink) Cursor(hotX, hotY, w, h int, rgba []byte) {}

func (s *tightLogSink) CutText(text string) {
	s.t.Logf("CutText: %q", text)
}
