package collections_test

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/collections"
	"github.com/braedonsaunders/ether/internal/core"
)

type blockingListCatalog struct {
	core.Catalog
	entered chan struct{}
	release chan struct{}
}

func (c *blockingListCatalog) List(ctx context.Context, list string, typ core.MediaType, limit int) ([]core.Title, error) {
	close(c.entered)
	select {
	case <-c.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []core.Title{{Key: core.MovieKey(101), Type: core.TypeMovie}}, nil
}

func TestEditingDuringBuildKeepsNewMembership(t *testing.T) {
	e := newEnv(t)
	cat := &blockingListCatalog{Catalog: e.cat, entered: make(chan struct{}), release: make(chan struct{})}
	svc, err := collections.New(e.db.(*sql.DB), cat, slog.Default(), collections.WithoutDefaults())
	if err != nil {
		t.Fatal(err)
	}
	sp := spec("Concurrent edit", "movie", []core.CollectionSource{{Kind: "tmdbList", List: "popular"}})
	sp.Enabled = false
	created, err := svc.CreateSpec(e.ctx, sp)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := svc.Rebuild(context.Background(), created.ID); done <- err }()
	select {
	case <-cat.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("build did not enter source")
	}
	sp.Enabled = true
	sp.Sources = []core.CollectionSource{{Kind: "titles", Keys: []string{core.MovieKey(102).String()}}}
	updated := make(chan error, 1)
	go func() { _, err := svc.UpdateSpec(context.Background(), created.ID, sp); updated <- err }()
	// Wait until the new definition is stored before releasing the old build.
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := svc.GetSpec(e.ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Enabled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("update not stored")
		}
		time.Sleep(time.Millisecond)
	}
	close(cat.release)
	if err := <-done; err == nil {
		t.Fatal("superseded build succeeded")
	}
	if err := <-updated; err != nil {
		t.Fatal(err)
	}
	page, err := svc.Items(e.ctx, owner, core.SmartKey(mustInt(t, created.ID)), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Key != core.MovieKey(102) {
		t.Fatalf("new definition served stale build: %+v", page.Items)
	}
}

func TestOtherServiceEditInvalidatesCachedMembership(t *testing.T) {
	e := newEnv(t)
	other, err := collections.New(e.db.(*sql.DB), e.cat, slog.Default(), collections.WithoutDefaults())
	if err != nil {
		t.Fatal(err)
	}
	sp := spec("Cross node", "movie", []core.CollectionSource{{Kind: "titles", Keys: []string{core.MovieKey(101).String()}}})
	created, err := e.svc.CreateSpec(e.ctx, sp)
	if err != nil {
		t.Fatal(err)
	}
	key := core.SmartKey(mustInt(t, created.ID))
	first, err := e.svc.Items(e.ctx, owner, key, 0, 10)
	if err != nil || len(first.Items) != 1 || first.Items[0].Key != core.MovieKey(101) {
		t.Fatalf("initial: %+v %v", first, err)
	}
	sp.Sources = []core.CollectionSource{{Kind: "titles", Keys: []string{core.MovieKey(102).String()}}}
	if _, err := other.UpdateSpec(e.ctx, created.ID, sp); err != nil {
		t.Fatal(err)
	}
	page, err := e.svc.Items(e.ctx, owner, key, 0, 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].Key != core.MovieKey(102) {
		t.Fatalf("stale after other service edit: %+v %v", page, err)
	}
}

func TestCancelPreviewWhileSourceBlocked(t *testing.T) {
	e := newEnv(t)
	cat := &blockingListCatalog{Catalog: e.cat, entered: make(chan struct{}), release: make(chan struct{})}
	svc, err := collections.New(e.db.(*sql.DB), cat, slog.Default(), collections.WithoutDefaults())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := svc.Preview(ctx, spec("Cancel in flight", "movie", []core.CollectionSource{{Kind: "tmdbList", List: "popular"}}), 10)
		done <- err
	}()
	select {
	case <-cat.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("source did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled preview did not return")
	}
}

type failedCatalog struct{ core.Catalog }

func (c failedCatalog) List(context.Context, string, core.MediaType, int) ([]core.Title, error) {
	return nil, errors.New("fixture list failure")
}
func (c failedCatalog) Collection(context.Context, int64) (string, []core.Title, error) {
	return "", nil, errors.New("fixture collection failure")
}
func TestCatalogListAndFranchiseFailurePreservesUnion(t *testing.T) {
	e := newEnv(t)
	svc, err := collections.New(e.db.(*sql.DB), failedCatalog{e.cat}, slog.Default(), collections.WithoutDefaults())
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []core.CollectionSource{{Kind: "tmdbList", List: "popular"}, {Kind: "tmdbCollection", ID: 10}} {
		t.Run(src.Kind, func(t *testing.T) {
			sp := spec("Failure union", "movie", []core.CollectionSource{{Kind: "titles", Keys: []string{core.MovieKey(101).String()}}, src})
			p, err := svc.Preview(e.ctx, sp, 10)
			if err != nil || len(p.Items) != 1 || p.Items[0].Key != core.MovieKey(101) || len(p.Warnings) == 0 {
				t.Fatalf("preview: %+v %v", p, err)
			}
		})
	}
}
