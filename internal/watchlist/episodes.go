package watchlist

import (
	"context"
	"fmt"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// New episodes of watchlisted shows. watchlist_episodes records, per viewer
// and show, the episodes already handled (found or present when the show
// became available), so each newer episode is processed once.
// watchlist_episodes_alerts claims each episode's owner alert once for every
// viewer, node and restart. Its own migration component keeps the numbering
// independent of the watchlist component's.
func init() {
	store.Register("watchlist_episodes", 1, `
CREATE TABLE watchlist_episodes(user_id INTEGER NOT NULL,item_key INTEGER NOT NULL,episode_key INTEGER NOT NULL,at INTEGER NOT NULL,PRIMARY KEY(user_id,item_key,episode_key));
CREATE TABLE watchlist_episodes_alerts(episode_key INTEGER PRIMARY KEY,at INTEGER NOT NULL);
`)
}

const (
	// freshAiring is how long after an episode airs its release is looked
	// for more often, at freshAiringCheck instead of the check interval.
	freshAiring      = 48 * time.Hour
	freshAiringCheck = time.Hour
)

// episodeRange returns the first and the newest released regular episodes
// of a show or season that u may see. Specials (season 0) air out of order
// and are often missing from indexers, so they are never "newest" and are
// "first" only for a show with nothing else released.
func (s *Service) episodeRange(ctx context.Context, u *core.User, t *core.Title) (first, newest *core.Title, err error) {
	var episodes []core.Title
	if t.Type == core.TypeSeason {
		episodes, err = s.o.Catalog.Children(ctx, t.Key)
	} else {
		episodes, err = s.o.Catalog.Grandchildren(ctx, t.Key)
	}
	if err != nil {
		return nil, nil, err
	}
	now := s.o.Now()
	var special *core.Title
	for i := range episodes {
		e := &episodes[i]
		if e.ContentRating == "" {
			e.ContentRating = t.ContentRating
		}
		if t.Adult {
			e.Adult = true
		}
		if !e.IsReleased(now) || !s.allowed(u, e) {
			continue
		}
		if e.Season <= 0 {
			if special == nil || e.Episode < special.Episode {
				special = e
			}
			continue
		}
		if first == nil || e.Season < first.Season || (e.Season == first.Season && e.Episode < first.Episode) {
			first = e
		}
		if e.Episode > 0 && (newest == nil || e.Season > newest.Season || (e.Season == newest.Season && e.Episode > newest.Episode)) {
			newest = e
		}
	}
	if first == nil {
		first = special
	}
	return first, newest, nil
}

func (s *Service) episodeKnown(ctx context.Context, userID int64, show, episode core.RatingKey) (bool, error) {
	var known bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM watchlist_episodes WHERE user_id=? AND item_key=? AND episode_key=?)`, userID, show, episode).Scan(&known)
	return known, err
}

// recordEpisode marks an episode handled for a viewer.
func (s *Service) recordEpisode(ctx context.Context, userID int64, show, episode core.RatingKey) error {
	_, err := s.claimEpisode(ctx, userID, show, episode)
	return err
}

// claimEpisode records an episode for a viewer and reports whether this call
// recorded it (false: already recorded, here or on another node).
func (s *Service) claimEpisode(ctx context.Context, userID int64, show, episode core.RatingKey) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO watchlist_episodes(user_id,item_key,episode_key,at) VALUES(?,?,?,?) ON CONFLICT(user_id,item_key,episode_key) DO NOTHING`, userID, show, episode, s.o.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// newEpisode looks for a release of an available show's newest episode.
// Once one is found the episode is recorded for the viewer, announced once
// (whichever viewer finds it first) and prewarmed. Until then the show is
// checked again on the next due pass; a failed alert leaves the episode
// unrecorded so it is retried.
func (s *Service) newEpisode(ctx context.Context, cfg Settings, u *core.User, show, episode *core.Title) error {
	res, e := s.resolve(ctx, u, episode)
	if _, err := s.db.ExecContext(ctx, `UPDATE watchlist_items SET checked=? WHERE user_id=? AND item_key=?`, s.o.Now().Unix(), u.ID, show.Key); err != nil {
		return err
	}
	if e != nil || res == nil {
		return nil
	}
	claimed, err := s.claimEpisode(ctx, u.ID, show.Key, episode.Key)
	if err != nil || !claimed {
		return err
	}
	if cfg.Notify && s.o.Notifier != nil {
		alert, err := s.db.ExecContext(ctx, `INSERT INTO watchlist_episodes_alerts(episode_key,at) VALUES(?,?) ON CONFLICT(episode_key) DO NOTHING`, episode.Key, s.o.Now().Unix())
		if err != nil {
			return err
		}
		if n, err := alert.RowsAffected(); err != nil {
			return err
		} else if n == 1 {
			if e := s.o.Notifier.TitleAvailable(ctx, episodeLabel(show, episode)); e != nil {
				s.o.Logger.Warn("watchlist: new episode alert failed", "episode", episode.Key, "err", e)
				_, err1 := s.db.ExecContext(ctx, `DELETE FROM watchlist_episodes_alerts WHERE episode_key=?`, episode.Key)
				_, err2 := s.db.ExecContext(ctx, `DELETE FROM watchlist_episodes WHERE user_id=? AND item_key=? AND episode_key=?`, u.ID, show.Key, episode.Key)
				if err1 != nil {
					return err1
				}
				return err2
			}
		}
	}
	if cfg.Prewarm && s.o.Engine != nil {
		if e := s.prewarm(ctx, res); e != nil {
			s.o.Logger.Debug("watchlist: prewarm new episode", "episode", episode.Key, "err", e)
		}
	}
	return nil
}

// episodeLabel names an episode in an alert: "Show S02E05 · Title".
func episodeLabel(show, episode *core.Title) string {
	name := show.Title
	if episode.ShowTitle != "" {
		name = episode.ShowTitle
	}
	label := fmt.Sprintf("%s S%02dE%02d", name, episode.Season, episode.Episode)
	if episode.Title != "" {
		label += " · " + episode.Title
	}
	return label
}
