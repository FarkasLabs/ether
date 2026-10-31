package plex

import (
	"context"

	"github.com/braedonsaunders/ether/internal/core"
)

// libraryNames contains only immutable display strings, never settings slices,
// viewer permissions or response state. Its lifetime is one bounded render.
type libraryNames struct{ movies, shows string }
type libraryNamesKey struct{}

func (s *Server) namesOrCurrent(names []libraryNames) libraryNames {
	if len(names) != 0 {
		return names[0]
	}
	c := s.cfg()
	return libraryNames{librarySectionTitle(core.TypeMovie, c), librarySectionTitle(core.TypeShow, c)}
}

func (n libraryNames) title(t core.MediaType) string {
	switch t {
	case core.TypeShow, core.TypeSeason, core.TypeEpisode:
		return n.shows
	default:
		return n.movies
	}
}

func (s *Server) renderLibraryNames(ctx context.Context) libraryNames {
	if n, ok := ctx.Value(libraryNamesKey{}).(libraryNames); ok {
		return n
	}
	return s.namesOrCurrent(nil)
}

func (s *Server) withRenderLibraryNames(ctx context.Context) context.Context {
	if _, ok := ctx.Value(libraryNamesKey{}).(libraryNames); ok {
		return ctx
	}
	return context.WithValue(ctx, libraryNamesKey{}, s.namesOrCurrent(nil))
}
