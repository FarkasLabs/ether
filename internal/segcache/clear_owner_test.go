package segcache

import (
	"context"
	"github.com/braedonsaunders/ether/internal/store"
	"testing"
)

func TestOwnerClearProtectedAndBlockedWrites(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "shared"}[shared], func(t *testing.T) {
			var c *Cache
			var err error
			if shared {
				db, e := store.OpenMemory(context.Background())
				if e != nil {
					t.Fatal(e)
				}
				defer db.Close()
				c, err = OpenShared(t.TempDir(), 1<<20, db, "node", nil)
			} else {
				c, err = Open(t.TempDir(), 1<<20, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if err = c.Put("x", 0, []byte("data")); err != nil {
				t.Fatal(err)
			}
			if err = c.Pin("x", "resume"); err != nil {
				t.Fatal(err)
			}
			if c.ClearUnpinned("x") == nil || !c.Has("x", 0) {
				t.Fatal("removed protected data")
			}
			if err = c.Unpin("x", "resume"); err != nil {
				t.Fatal(err)
			}
			c.SetBlockCheck(func(id string) bool { return id == "x" })
			if err = c.ClearUnpinned("x"); err != nil {
				t.Fatal(err)
			}
			if c.Has("x", 0) {
				t.Fatal("data remains")
			}
			if err := c.Put("x", 1, []byte("data")); err != nil || c.Has("x", 1) {
				t.Fatal("blocked data retained or playback failed")
			}
		})
	}
}
