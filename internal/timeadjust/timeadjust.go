package timeadjust

import (
	"sort"
	"sync"
	"time"
)

// MaxAllowedOffset is the maximum median offset we'll apply (70 minutes),
// matching Bitcoin Core's behavior. If the median exceeds this, we log a
// warning but refuse to adjust — the local clock is assumed broken.
const MaxAllowedOffset = 70 * time.Minute

// MinSamples is the number of peer samples required before the median offset
// is used. Until this threshold is reached, the offset is zero (local clock).
const MinSamples = 5

// MaxSamples caps the number of peer offsets retained. Bitcoin Core uses 200.
const MaxSamples = 200

// AdjustedClock provides network-adjusted time, modeled after Bitcoin Core's
// CTimeData / GetAdjustedTime(). Peer timestamps from version messages are
// compared against local time to compute offsets. The median offset is applied
// to all subsequent time queries once enough samples are collected.
type AdjustedClock struct {
	mu      sync.RWMutex
	offsets []int64
	known   map[string]struct{}
	median  int64
	warned  bool
}

// New creates a new AdjustedClock with the local node's own zero offset
// pre-seeded (Bitcoin Core seeds offset 0 for the local node).
func New() *AdjustedClock {
	return &AdjustedClock{
		offsets: []int64{0},
		known:   make(map[string]struct{}),
	}
}

// AddSample records a peer's time offset. addr is used to deduplicate — each
// peer address contributes at most one sample. peerTime is the timestamp from
// the peer's version message.
func (c *AdjustedClock) AddSample(addr string, peerTime int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, seen := c.known[addr]; seen {
		return
	}
	c.known[addr] = struct{}{}

	offset := peerTime - time.Now().Unix()

	if len(c.offsets) >= MaxSamples {
		c.offsets = c.offsets[1:]
	}
	c.offsets = append(c.offsets, offset)

	c.recalcMedian()
}

func (c *AdjustedClock) recalcMedian() {
	sorted := make([]int64, len(c.offsets))
	copy(sorted, c.offsets)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	med := sorted[len(sorted)/2]

	if len(c.offsets) >= MinSamples {
		if med > int64(MaxAllowedOffset.Seconds()) || med < -int64(MaxAllowedOffset.Seconds()) {
			c.median = 0
			c.warned = true
		} else {
			c.median = med
		}
	}
}

// Now returns the network-adjusted current time as a Unix timestamp.
func (c *AdjustedClock) Now() int64 {
	c.mu.RLock()
	offset := c.median
	c.mu.RUnlock()
	return time.Now().Unix() + offset
}

// Offset returns the current median offset in seconds.
func (c *AdjustedClock) Offset() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.median
}

// SampleCount returns the number of peer time samples collected.
func (c *AdjustedClock) SampleCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.offsets)
}
