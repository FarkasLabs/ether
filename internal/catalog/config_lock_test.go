package catalog

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestUpdateConfigDoesNotBlockConfigReaderWhileWaitingForDB(t *testing.T) {
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	c, err := New(db, nil, config.Catalog{GateMoviesByHomeRelease: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	done := make(chan struct{})
	go func() { c.UpdateConfig(config.Catalog{}); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for db.Stats().WaitCount == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if db.Stats().WaitCount == 0 {
		t.Fatal("update did not wait for the held connection")
	}
	read := make(chan struct{})
	go func() { _ = c.config(); close(read) }()
	select {
	case <-read:
	case <-time.After(time.Second):
		t.Error("config reader blocked behind database wait")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("update did not finish")
	}
	if c.config().GateMoviesByHomeRelease {
		t.Fatal("config was not applied")
	}
}

func TestUpdateConfigAppliesSettingsOnAdmissionFailure(t *testing.T) {
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c, err := New(db, nil, config.Catalog{GateMoviesByHomeRelease: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP TABLE catalog_home_release`); err != nil {
		t.Fatal(err)
	}
	c.UpdateConfig(config.Catalog{MinVotes: 42})
	if got := c.config(); got.GateMoviesByHomeRelease || got.MinVotes != 42 {
		t.Fatalf("settings not applied: %+v", got)
	}
	if !c.admissionsPending {
		t.Fatal("failed admission was not marked for retry")
	}
	if _, err := db.Exec(`CREATE TABLE catalog_home_release (key INTEGER PRIMARY KEY, checked INTEGER NOT NULL DEFAULT 0, earliest_ts INTEGER NOT NULL DEFAULT 0, fallback_admitted INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO catalog_titles(key,type,released_ts) VALUES(1,'movie',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO catalog_home_release(key,checked) VALUES(1,1)`); err != nil {
		t.Fatal(err)
	}
	c.UpdateConfig(config.Catalog{GateMoviesByHomeRelease: true})
	var admitted int
	if err := db.QueryRow(`SELECT fallback_admitted FROM catalog_home_release WHERE key=1`).Scan(&admitted); err != nil || admitted != 1 || c.admissionsPending {
		t.Fatalf("retry: admitted=%d pending=%v err=%v", admitted, c.admissionsPending, err)
	}
}

func TestUpdateConfigSerializesTransitionsAndPreservesAdmission(t *testing.T) {
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	c, err := New(db, nil, config.Catalog{GateMoviesByHomeRelease: true}, nil, WithNow(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO catalog_titles(key,type,released_ts) VALUES(1,'movie',?)`, now.Add(-100*24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO catalog_home_release(key,checked,earliest_ts) VALUES(1,1,0)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	first := make(chan struct{})
	go func() { c.UpdateConfig(config.Catalog{MinVotes: 1}); close(first) }()
	deadline := time.Now().Add(5 * time.Second)
	for db.Stats().WaitCount == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if db.Stats().WaitCount == 0 {
		t.Fatal("first update did not wait")
	}
	second := make(chan struct{})
	go func() { c.UpdateConfig(config.Catalog{GateMoviesByHomeRelease: true, MinVotes: 2}); close(second) }()
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, done := range []chan struct{}{first, second} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("update stuck")
		}
	}
	var admitted int
	if err := db.QueryRow(`SELECT fallback_admitted FROM catalog_home_release WHERE key=1`).Scan(&admitted); err != nil || admitted != 1 {
		t.Fatalf("admission = %d, err = %v", admitted, err)
	}
	if got := c.config(); !got.GateMoviesByHomeRelease || got.MinVotes != 2 {
		t.Fatalf("updates out of order: %+v", got)
	}
}
