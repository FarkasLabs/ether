package core

import "context"

// ReleaseBlockChecker lets stored source consumers honor durable owner blocks.
// Errors must prevent reuse, rather than silently bypassing the blocklist.
type ReleaseBlockChecker interface {
	ReleaseBlocked(context.Context, string) (bool, error)
}
