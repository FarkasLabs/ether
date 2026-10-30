package segcache

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/store"
)

// fakeVolume is a disk of total bytes holding the cache plus other files.
type fakeVolume struct {
	total int64
	other atomic.Int64
	fail  atomic.Bool
	stats atomic.Int32
	cache func() int64
}

func (v *fakeVolume) usage() (int64, int64, error) {
	v.stats.Add(1)
	if v.fail.Load() {
		return 0, 0, errors.New("statfs: input/output error")
	}
	return v.total, v.total - v.other.Load() - v.cache(), nil
}

func TestMinFreeBytes(t *testing.T) {
	for total, want := range map[int64]int64{0: 0, 1000: 50, 100 << 30: 5 << 30, 4 << 40: 10 << 30} {
		if got := MinFreeBytes(total); got != want {
			t.Errorf("MinFreeBytes(%d)=%d want %d", total, got, want)
		}
	}
}

func TestFreeSpaceFloorEvictsOldestUnprotected(t *testing.T) {
	c := openT(t, t.TempDir(), 0) // unlimited budget
	now := time.Unix(1_800_000_000, 0)
	c.now = func() time.Time { return now }
	vol := &fakeVolume{total: 1000} // floor: 50 bytes
	vol.cache = func() int64 { used, _ := c.Usage(); return used }
	c.SetDiskUsage(vol.usage)
	for i := 0; i < 10; i++ {
		mustPut(t, c, "old", i, payload("old", i, 1, 10))
	}
	if err := c.Pin("pinned", "owner"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		mustPut(t, c, "pinned", i, payload("pinned", i, 1, 10))
	}
	for i := 0; i < 5; i++ {
		mustPut(t, c, "new", i, payload("new", i, 1, 10))
	}
	if used, _ := c.Usage(); used != 200 {
		t.Fatalf("evicted with plenty of space: %d", used)
	}
	// Another program fills the disk: 700 + 200 cache leaves 100 free.
	vol.other.Store(700)
	now = now.Add(floorStatEvery)
	c.evict()
	if used, _ := c.Usage(); used != 200 {
		t.Fatalf("evicted above the floor: %d", used)
	}
	// 780 + 200 leaves 20 free, 30 short of the 50-byte floor.
	vol.other.Store(780)
	now = now.Add(floorStatEvery)
	c.evict()
	if used, _ := c.Usage(); used != 170 {
		t.Fatalf("used %d after floor eviction, want 170", used)
	}
	for i := 0; i < 3; i++ {
		if c.Has("old", i) {
			t.Fatalf("oldest segment %d kept", i)
		}
	}
	if !c.Has("old", 3) || !c.Has("new", 4) {
		t.Fatal("evicted more than the shortfall")
	}
	// Repeated passes before the next measurement do not free it again.
	c.evict()
	c.evict()
	if used, _ := c.Usage(); used != 170 {
		t.Fatalf("double eviction: %d", used)
	}
	// Writes between measurements count against the floor at once.
	mustPut(t, c, "new", 5, payload("new", 5, 1, 10))
	if used, _ := c.Usage(); used != 170 || c.Has("old", 3) {
		t.Fatalf("write between measurements not offset: used %d", used)
	}
	// The disk fills completely: everything unprotected goes, pins stay.
	vol.other.Store(1000)
	now = now.Add(floorStatEvery)
	c.evict()
	if used, _ := c.Usage(); used != 50 {
		t.Fatalf("used %d, want only the pinned 50", used)
	}
	for i := 0; i < 5; i++ {
		if !c.Has("pinned", i) {
			t.Fatal("pinned segment evicted for the floor")
		}
	}
	if floor, free := c.FreeFloor(); floor != 50 || free != -170 {
		t.Fatalf("FreeFloor %d %d", floor, free)
	}
}

func TestFreeSpaceFloorRespectsProtectionAndPausesOnError(t *testing.T) {
	c := openT(t, t.TempDir(), 0)
	now := time.Unix(1_800_000_000, 0)
	c.now = func() time.Time { return now }
	vol := &fakeVolume{total: 1000}
	vol.cache = func() int64 { used, _ := c.Usage(); return used }
	c.Protect(func() map[string]bool { return map[string]bool{"playing": true} })
	for i := 0; i < 5; i++ {
		mustPut(t, c, "playing", i, payload("playing", i, 1, 10))
	}
	vol.other.Store(990)
	vol.fail.Store(true)
	c.SetDiskUsage(vol.usage)
	if used, _ := c.Usage(); used != 50 {
		t.Fatalf("evicted on a failed measurement: %d", used)
	}
	vol.fail.Store(false)
	now = now.Add(floorStatEvery)
	c.evict()
	if used, _ := c.Usage(); used != 50 {
		t.Fatalf("evicted an active stream for the floor: %d", used)
	}
	// Measurements are rate limited.
	before := vol.stats.Load()
	for i := 0; i < 20; i++ {
		c.evict()
	}
	if vol.stats.Load() != before {
		t.Fatalf("measured %d times within %v", vol.stats.Load()-before, floorStatEvery)
	}
	c.SetDiskUsage(nil)
	if floor, free := c.FreeFloor(); floor != 0 || free != 0 {
		t.Fatal("floor still reported after disabling")
	}
}

func TestSharedCacheFreeSpaceFloor(t *testing.T) {
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c, err := OpenShared(t.TempDir(), 0, db, "node-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	now := time.Unix(1_800_000_000, 0)
	c.now = func() time.Time { return now }
	var shared atomic.Int64
	vol := &fakeVolume{total: 1000}
	vol.cache = shared.Load
	for i := 0; i < 10; i++ {
		if err := c.Put("film", i, payload("film", i, 1, 10)); err != nil {
			t.Fatal(err)
		}
	}
	shared.Store(100)
	vol.other.Store(880) // 20 free: 30 short
	c.SetDiskUsage(vol.usage)
	n := 0
	for i := 0; i < 10; i++ {
		if c.Has("film", i) {
			n++
		}
	}
	if n != 7 {
		t.Fatalf("%d segments left, want 7", n)
	}
	// The next sweep before a new measurement credits what was freed.
	c.Sweep()
	n = 0
	for i := 0; i < 10; i++ {
		if c.Has("film", i) {
			n++
		}
	}
	if n != 7 {
		t.Fatalf("shared double eviction: %d left", n)
	}
}
