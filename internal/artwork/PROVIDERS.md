# Owner-configured poster sources

Disabled by default. `config.Posters` is shared configuration. The current
snapshot is supplied through `ImagesOptions.Posters` in app `collectionOptions`,
so saved settings take effect without restarting.
The existing overlay image source detects `GetPoster` and `PosterVersion`.

Verified primary sources (2026-10-28):

- PostersPlus: https://github.com/UmbraProjects/PostersPlus/blob/dev/URL_REFERENCE.md
  `/poster?tmdb_id=ID&type=movie|tv&access_key=KEY`. Owner supplies the instance
  base URL and optional access key, and configures its style on that instance.
- BetterPosters: https://btttr.cc/configure — the page's `updateAioUrl` exports
  `https://btttr.cc/poster/imdb/poster-default/{imdb_id}.jpg` by default, with
  optional path/query style settings. Paste its exported URL for customization.
- RPDB: https://rpdb.docs.apiary.io/ (announces migration to https://rpdb.apidoc.io/)
  `https://api.ratingposterdb.com/KEY/tmdb/poster-default/movie-ID.jpg`, or
  `series-ID.jpg` for shows. Owner supplies a key; account tier limits apply.
- MDBList: https://api.mdblist.com/docs/ documents metadata APIs. No dedicated
  image-rendering endpoint was verified, so no fabricated MDBList image preset
  is shipped. Ether's existing ratings integration remains separate.

Custom HTTP(S) templates support `{imdb_id}`, `{tmdb_id}`, `{type}` (movie/tv),
and `{api_key}`. A TMDB placeholder requires the type placeholder. Missing IDs
skip the provider. Seasons and episodes keep their own TMDB artwork; their IDs
are never sent as movie or show IDs. Templates and keys are sealed and redacted.
Changing destination requires re-entering retained credentials.

Owner-configured LAN hosts are intentional. Hosts are fixed in templates,
userinfo and fragments are forbidden, and all redirects are refused. Requests
have an eight-second deadline, two active fetch/decode slots, an 8 MiB response
limit and a 16-megapixel decode limit (8192 per dimension). Output is a resized
JPEG. Provider errors and bodies are never logged or returned to clients.

The existing memory/disk cache is keyed by a SHA-256 digest of all settings,
resolved URL and width, with six-hour generations for upstream changes. Failed
provider requests fall back to TMDB without populating the provider cache.
Ether overlays composite the resulting image, so disable overlapping badges
in either service. The saved-source artwork preview uses the same pipeline.
Plex thumbnail URLs carry only opaque versions. Provider responses use content
ETags and revalidation to avoid making transient TMDB fallbacks permanent.

Limitations: no live account tests or supplied credentials; no cross-origin or
same-origin redirects; no season/episode-specific provider artwork; no provider
style editor (use provider configurators). Collection mosaics continue to use
TMDB. Plex clients may keep previously downloaded thumbnails until refresh.
