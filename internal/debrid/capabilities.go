package debrid

// knownTorrentFinder is a package-specific optional capability.
type knownTorrentFinder interface{ findKnown(string) string }

// backgroundCacheOnlyProvider is a provider whose Acquire, with
// CachedOnly set and no torrent id, serves already-cached content
// without adding anything to the account (Premiumize's directdl).
// Only such providers may proceed in a background check after a
// positive CheckCached: every other provider's id-less Acquire adds
// a torrent, which background work must never do.
type backgroundCacheOnlyProvider interface{ supportsBackgroundCacheOnly() bool }
