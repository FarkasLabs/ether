package catalog

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

func TestDetailedFutureSummaryCorrection(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := &releaseClient{details: map[int64]*tmdb.MovieDetails{1: releaseDetail(t, `{"id":1,"title":"Film","release_date":"2035-04-10","overview":"Details"}`)}}
	c, err := New(db, client, config.Catalog{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.getMovie(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	title, err := c.StoredTitle(ctx, core.MovieKey(1))
	if err != nil {
		t.Fatal(err)
	}
	client.details[1] = releaseDetail(t, `{"id":1,"title":"Film","release_date":"2035-05-10","overview":"Details"}`)
	title.Released = time.Date(2035, 5, 10, 0, 0, 0, 0, time.UTC)
	if _, err := c.RefreshTitle(ctx, title.Key, true); err != nil {
		t.Fatal(err)
	}
	got, err := c.StoredTitle(ctx, title.Key)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Released.Equal(title.Released) {
		t.Fatalf("correction ignored: got %v want %v", got.Released, title.Released)
	}
}
