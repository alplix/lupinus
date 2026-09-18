package rfb

import (
	"testing"
	"time"
)

// newTunerClient returns a Client positioned for noteUpdate: a 2880x1800
// screen (the size macOS Screen Sharing reports on a Retina MacBook) with no
// connection, since noteUpdate only touches its own bookkeeping.
func newTunerClient(quality string) *Client {
	return &Client{quality: quality, width: 2880, height: 1800, lastFast: false}
}

// fullFrame reports a full-screen update of the given wire size that took
// `wait` blocked on the network.
func fullFrame(c *Client, wireBytes int64, wait time.Duration) {
	c.noteUpdate(wait+50*time.Millisecond, wait, wireBytes, c.width*c.height, false)
}

func TestTunerDropsDepthOnSlowLink(t *testing.T) {
	c := newTunerClient("balanced")
	// A 5MB frame arriving at 4MB/s: 1.25s per frame at level 0.
	fullFrame(c, 5<<20, 1250*time.Millisecond)
	if c.wantLevel != len(pixelLevels)-1 {
		t.Fatalf("wantLevel = %d after a slow full frame, want %d", c.wantLevel, len(pixelLevels)-1)
	}
	if c.lastFast {
		t.Error("a big slow update should turn pipelining off")
	}
}

func TestTunerKeepsFullColourOnFastLink(t *testing.T) {
	c := newTunerClient("balanced")
	// The same 5MB frame at 100MB/s: 50ms.
	fullFrame(c, 5<<20, 50*time.Millisecond)
	if c.wantLevel != 0 {
		t.Fatalf("wantLevel = %d on a fast link, want 0", c.wantLevel)
	}
	if !c.lastFast {
		t.Error("a fast update should leave pipelining on")
	}
}

func TestTunerQualityPresetNeverAdapts(t *testing.T) {
	c := newTunerClient("quality")
	fullFrame(c, 5<<20, 3*time.Second)
	if c.wantLevel != 0 {
		t.Fatalf("quality preset changed level to %d", c.wantLevel)
	}
}

func TestTunerIgnoresTight(t *testing.T) {
	c := newTunerClient("balanced")
	c.noteUpdate(3*time.Second, 3*time.Second, 5<<20, c.width*c.height, true)
	if c.wantLevel != 0 {
		t.Fatalf("Tight update changed level to %d", c.wantLevel)
	}
}

func TestTunerClimbsBackWhenLinkRecovers(t *testing.T) {
	c := newTunerClient("balanced")
	fullFrame(c, 5<<20, 1250*time.Millisecond)
	c.level, c.sentLevel = c.wantLevel, c.wantLevel // what handleFramebufferUpdate does next
	// Link now fast: level-2 frames are 1.2MB in 20ms. Wait out the cooldown
	// and let the smoothed throughput catch up.
	for i := 0; i < 40 && c.wantLevel == c.level; i++ {
		fullFrame(c, 1250<<10, 20*time.Millisecond)
	}
	if c.wantLevel >= c.level {
		t.Fatalf("tuner never climbed back on a fast link: level %d want %d", c.level, c.wantLevel)
	}
}

func TestTunerBacksOffAfterFailedClimb(t *testing.T) {
	c := newTunerClient("balanced")
	fullFrame(c, 5<<20, 1250*time.Millisecond)
	c.level, c.sentLevel = c.wantLevel, c.wantLevel
	for i := 0; i < 40 && c.wantLevel == c.level; i++ {
		fullFrame(c, 1250<<10, 20*time.Millisecond)
	}
	climbedTo := c.wantLevel
	c.level, c.sentLevel = climbedTo, climbedTo

	// The higher level turns out too slow: the tuner drops back...
	for i := 0; i < 40 && c.wantLevel == c.level; i++ {
		fullFrame(c, 5<<20, 1250*time.Millisecond)
	}
	if c.wantLevel <= climbedTo {
		t.Fatalf("tuner did not drop back from level %d", climbedTo)
	}
	// ...and must not immediately try the failed level again.
	if !time.Now().Before(c.upBlocked[climbedTo]) {
		t.Errorf("no backoff recorded for level %d", climbedTo)
	}
}
