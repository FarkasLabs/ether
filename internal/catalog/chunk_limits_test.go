package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func newChunkTestCatalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := New(store.OpenTest(t), nil, config.Catalog{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func upsertGenreMovies(t *testing.T, ctx context.Context, c *Catalog, n int) []core.Title {
	t.Helper()
	titles := make([]core.Title, 0, n)
	for i := 0; i < n; i++ {
		titles = append(titles, core.Title{
			Key:      core.MovieKey(int64(i + 1)),
			Type:     core.TypeMovie,
			Title:    fmt.Sprintf("Movie %04d", i),
			Released: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
			Genres:   []string{fmt.Sprintf("genre-%04d", i)},
		})
	}
	if err := c.upsertMany(ctx, titles, upsertOpts{browsable: true}); err != nil {
		t.Fatal(err)
	}
	return titles
}

// Mais itens que qualquer chunk do pacote (200/400/500) e que o limite
// clássico de variáveis do SQLite (999): todos devem voltar hidratados,
// na ordem exata da fatia de entrada.
func TestHydrateSliceBeyondChunkReturnsAllInOrder(t *testing.T) {
	ctx := context.Background()
	c := newChunkTestCatalog(t)
	const n = 1001
	titles := upsertGenreMovies(t, ctx, c, n)

	// Entrada em ordem reversa: hydrate não pode reordenar a fatia.
	items := make([]core.Title, 0, n)
	for i := n - 1; i >= 0; i-- {
		items = append(items, core.Title{Key: titles[i].Key, Type: core.TypeMovie})
	}
	if err := c.hydrateSlice(ctx, items); err != nil {
		t.Fatal(err)
	}
	if len(items) != n {
		t.Fatalf("hydrate returned %d items, want %d", len(items), n)
	}
	for pos, item := range items {
		want := titles[n-1-pos]
		if item.Key != want.Key {
			t.Fatalf("position %d: key %v, want %v (order changed)", pos, item.Key, want.Key)
		}
		if len(item.Genres) != 1 || item.Genres[0] != want.Genres[0] {
			t.Fatalf("position %d (key %v): genres %v, want %v", pos, item.Key, item.Genres, want.Genres)
		}
	}
}

// Teste do dono: a mesma key em dois chunks diferentes (posições 0 e 251,
// com chunk de 200) — TODAS as ocorrências recebem o facet, exatamente
// uma vez cada. Com mapa key->index único, a primeira cópia ficaria sem
// genre e a última com genre duplicado.
func TestHydrateSliceDuplicateKeyAcrossChunks(t *testing.T) {
	ctx := context.Background()
	c := newChunkTestCatalog(t)
	const n = 250
	titles := upsertGenreMovies(t, ctx, c, n)

	dup := titles[0] // genre-0000
	items := make([]core.Title, 0, n+2)
	items = append(items, core.Title{Key: dup.Key, Type: core.TypeMovie})
	for _, title := range titles {
		items = append(items, core.Title{Key: title.Key, Type: core.TypeMovie})
	}
	items = append(items, core.Title{Key: dup.Key, Type: core.TypeMovie})

	if err := c.hydrateSlice(ctx, items); err != nil {
		t.Fatal(err)
	}
	for pos, item := range items {
		if len(item.Genres) != 1 {
			t.Fatalf("position %d (key %v): genres %v, want exactly 1", pos, item.Key, item.Genres)
		}
	}
	for _, pos := range []int{0, 1, len(items) - 1} {
		if items[pos].Key != dup.Key || items[pos].Genres[0] != "genre-0000" {
			t.Fatalf("occurrence at %d: key %v genres %v, want %v [genre-0000]", pos, items[pos].Key, items[pos].Genres, dup.Key)
		}
	}
}

// Mais shows que o batch de 200 de airedCounts: as contagens aired de
// shows E seasons além do primeiro chunk devem vir certas, na ordem da
// fatia. Os itens começam com o sentinela 999 — se só o primeiro chunk
// fosse processado (mutação), os demais manteriam 999 e o teste falha.
func TestAiredCountsBeyondChunkCountsShowsAndSeasons(t *testing.T) {
	ctx := context.Background()
	c := newChunkTestCatalog(t)
	const n = 250
	past := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	future := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	// wantEpisodes varia por show pra detectar troca de contagem entre keys.
	wantEpisodes := func(id int64) int { return 1 + int(id%2) }

	stored := make([]core.Title, 0, n*4)
	for id := int64(1); id <= n; id++ {
		show := core.ShowKey(id)
		stored = append(stored, core.Title{
			Key: core.SeasonKey(id, 1), Type: core.TypeSeason, ShowKey: show, Season: 1,
			Title: fmt.Sprintf("Season 1 of %04d", id), Released: past, ChildCount: 99,
		})
		for ep := 1; ep <= 3; ep++ {
			released := future
			if ep <= wantEpisodes(id) {
				released = past
			}
			stored = append(stored, core.Title{
				Key: core.EpisodeKey(id, 1, ep), Type: core.TypeEpisode, ShowKey: show,
				Season: 1, Episode: ep, Title: fmt.Sprintf("E%d of %04d", ep, id), Released: released,
			})
		}
	}
	if err := c.upsertMany(ctx, stored, upsertOpts{browsable: true}); err != nil {
		t.Fatal(err)
	}

	// Ordem reversa, shows e seasons intercalados: airedCounts não pode
	// reordenar a fatia nem perder os chunks após o primeiro.
	items := make([]core.Title, 0, 2*n)
	for id := int64(n); id >= 1; id-- {
		items = append(items,
			core.Title{Key: core.ShowKey(id), Type: core.TypeShow, ChildCount: 999, LeafCount: 999},
			core.Title{Key: core.SeasonKey(id, 1), Type: core.TypeSeason, ChildCount: 999},
		)
	}
	if err := c.airedCounts(ctx, items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 2*n {
		t.Fatalf("airedCounts returned %d items, want %d", len(items), 2*n)
	}
	for pos, item := range items {
		id := int64(n - pos/2)
		want := wantEpisodes(id)
		if pos%2 == 0 {
			if item.Key != core.ShowKey(id) || item.Type != core.TypeShow {
				t.Fatalf("position %d: key/type %v/%v, want show %d (order changed)", pos, item.Key, item.Type, id)
			}
			if item.ChildCount != 1 || item.LeafCount != want {
				t.Fatalf("position %d (show %d): counts %d seasons/%d episodes, want 1/%d", pos, id, item.ChildCount, item.LeafCount, want)
			}
		} else {
			if item.Key != core.SeasonKey(id, 1) || item.Type != core.TypeSeason {
				t.Fatalf("position %d: key/type %v/%v, want season 1 of %d (order changed)", pos, item.Key, item.Type, id)
			}
			if item.ChildCount != want {
				t.Fatalf("position %d (season of show %d): childCount %d, want %d aired", pos, id, item.ChildCount, want)
			}
		}
	}
}

// A faceta genre NÃO tem cap: o bound é só de parâmetro SQL, não de
// resultado. Com 205 genres distintos, todos voltam.
func TestFacetGenreReturnsAllBeyond200(t *testing.T) {
	ctx := context.Background()
	c := newChunkTestCatalog(t)
	upsertGenreMovies(t, ctx, c, 205)

	vals, err := c.Facet(ctx, core.TypeMovie, "genre")
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != 205 {
		t.Fatalf("genre facet returned %d values, want all 205 (no cap)", len(vals))
	}
	// Todos com Count=1: desempate por genre ASC.
	if vals[0].Key != "genre-0000" || vals[204].Key != "genre-0204" {
		t.Fatalf("genre facet bounds = %q..%q, want genre-0000..genre-0204", vals[0].Key, vals[204].Key)
	}
	for _, v := range vals {
		if v.Count != 1 {
			t.Fatalf("genre %q count = %d, want 1", v.Key, v.Count)
		}
	}
}

// FilterVisibility enriquece genres/countries com IN limitado: um título
// além do primeiro chunk cujo genre armazenado é excluído deve ser
// escondido, e os mantidos além do chunk devem vir enriquecidos.
func TestFilterVisibilityBeyondChunk(t *testing.T) {
	ctx := context.Background()
	specs := []core.LibraryExclusion{{
		Name: "hide last genre", MediaType: "mixed", Match: "all",
		Rules: []core.CollectionRule{{Field: "genre", Op: "in", Value: json.RawMessage(`["genre-0249"]`)}},
	}}
	c, err := New(store.OpenTest(t), nil, config.Catalog{Exclusions: specs}, nil)
	if err != nil {
		t.Fatal(err)
	}
	const n = 250
	titles := upsertGenreMovies(t, ctx, c, n)

	summaries := make([]core.Title, 0, n)
	for _, title := range titles {
		summaries = append(summaries, core.Title{Key: title.Key, Type: core.TypeMovie, Title: title.Title})
	}
	got := c.FilterVisibility(ctx, summaries)
	if len(got) != n-1 {
		t.Fatalf("FilterVisibility kept %d, want %d (genre-0249 beyond first chunk hidden)", len(got), n-1)
	}
	for _, item := range got {
		if item.Key == titles[n-1].Key {
			t.Fatalf("title with hidden genre %v was kept", item.Key)
		}
	}
	// Mantido além do primeiro chunk (posição 200) veio enriquecido.
	beyond := got[200]
	if len(beyond.Genres) != 1 || beyond.Genres[0] != "genre-0200" {
		t.Fatalf("kept title beyond chunk: genres %v, want [genre-0200]", beyond.Genres)
	}
}

// Target selection must not depend on input order or which side of the query
// boundary a source occupies. Within a source, stored recommendation rank wins.
func TestCrowdGraphBatchStableTargetSelection(t *testing.T) {
	ctx := context.Background()
	c := newChunkTestCatalog(t)
	const sources = 205
	targets := upsertGenreMovies(t, ctx, c, 2*sources)
	keys := make([]core.RatingKey, sources)
	for i := range keys {
		keys[i] = core.MovieKey(int64(10000 + i))
		recs := fmt.Sprintf("%d,%d", targets[2*i+1].Key, targets[2*i].Key)
		if _, err := c.db.ExecContext(ctx, `INSERT INTO catalog_crowd(key,fetched_at,pages,recs) VALUES(?,?,?,?)`, int64(keys[i]), 1, 1, recs); err != nil {
			t.Fatal(err)
		}
	}
	reversed := append([]core.RatingKey(nil), keys...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	rotated := append(append([]core.RatingKey(nil), keys[199:]...), keys[:199]...)
	for _, cap := range []int{3, 401} {
		for _, input := range [][]core.RatingKey{keys, reversed, rotated} {
			edges, loaded, err := c.CrowdGraphBatch(ctx, input, 2, cap)
			if err != nil {
				t.Fatal(err)
			}
			if len(edges) != sources || len(loaded) != cap {
				t.Fatalf("cap %d: %d edges and %d titles", cap, len(edges), len(loaded))
			}
			for i := range keys {
				want := []core.RatingKey{targets[2*i+1].Key, targets[2*i].Key}
				if len(edges[keys[i]]) != 2 || edges[keys[i]][0] != want[0] || edges[keys[i]][1] != want[1] {
					t.Fatalf("source %d: edges %v, want %v", i, edges[keys[i]], want)
				}
				for j, key := range want {
					_, found := loaded[key]
					if found != (2*i+j < cap) {
						t.Fatalf("cap %d: target %v found=%v", cap, key, found)
					}
				}
			}
		}
	}
}
