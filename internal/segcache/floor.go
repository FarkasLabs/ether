package segcache

import "time"

// Free-space floor. The budget is the most the owner lets the cache use, but
// the volume it lives on is shared with everything else (the system drive, a
// NAS share) and may be smaller than the budget or fill up from other
// writers. Eviction therefore also keeps a floor of the volume free: the
// smaller of 10 GiB and 5% of the volume. Pinned and protected releases are
// never evicted for it; if only they remain, the cache logs and waits.
const (
	floorMaxBytes  = 10 << 30
	floorFraction  = 20 // 5%
	floorStatEvery = 2 * time.Second
)

// DiskUsageFunc reports the size and the free bytes of the cache's volume.
type DiskUsageFunc func() (total, free int64, err error)

// MinFreeBytes is the free space the cache leaves on a volume of total bytes.
func MinFreeBytes(total int64) int64 { return min(floorMaxBytes, max(total, 0)/floorFraction) }

// floorStat is one measurement of the volume.
type floorStat struct {
	ok      bool
	at      time.Time
	need    int64 // floor minus free space when measured; >0 means short
	usedAt  int64 // cache usage when measured
	floor   int64
	free    int64
	failing bool // the last measurement failed (logged once)
}

// SetDiskUsage enables the free-space floor; nil disables it. It applies
// immediately.
func (c *Cache) SetDiskUsage(fn DiskUsageFunc) {
	c.floorMu.Lock()
	c.diskFn = fn
	c.floor = floorStat{}
	c.floorMu.Unlock()
	c.evict()
}

// FreeFloor reports the floor and the free space last measured (0, 0 while
// the floor is off or unmeasured).
func (c *Cache) FreeFloor() (floor, free int64) {
	c.floorMu.Lock()
	defer c.floorMu.Unlock()
	if !c.floor.ok {
		return 0, 0
	}
	return c.floor.floor, c.floor.free
}

// floorNeed returns how many bytes eviction must free to restore the floor
// (<=0: none), measuring the volume at most every floorStatEvery. Between
// measurements the cache's own growth or shrinkage since the last one is
// counted in, so a burst of writes or an eviction is never missed or
// double-counted.
func (c *Cache) floorNeed() int64 {
	c.floorMu.Lock()
	defer c.floorMu.Unlock()
	if c.diskFn == nil {
		return 0
	}
	now := c.now()
	if !c.floor.ok || now.Sub(c.floor.at) >= floorStatEvery || now.Before(c.floor.at) {
		total, free, err := c.diskFn()
		c.mu.Lock()
		used := c.used
		c.mu.Unlock()
		if err != nil || total <= 0 {
			if !c.floor.failing {
				c.logger.Warn("segment cache: cannot read free space; the free-space floor is paused", "err", err)
			}
			c.floor = floorStat{failing: true}
			return 0
		}
		floor := MinFreeBytes(total)
		c.floor = floorStat{ok: true, at: now, need: floor - free, usedAt: used, floor: floor, free: free}
	}
	c.mu.Lock()
	grown := c.used - c.floor.usedAt
	c.mu.Unlock()
	return c.floor.need + grown
}

// floorFreed credits bytes freed outside c.used accounting (the shared
// cache) against the last measurement.
func (c *Cache) floorFreed(n int64) {
	if n <= 0 {
		return
	}
	c.floorMu.Lock()
	if c.floor.ok {
		c.floor.need -= n
	}
	c.floorMu.Unlock()
}
