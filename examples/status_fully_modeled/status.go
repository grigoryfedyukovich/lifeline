package status_fully_modeled

import "context"

// The cancel function is deferred right after creation: verified, with
// nothing handed off.
func Start(parent context.Context) {
	_, cancel := context.WithCancel(parent)
	defer cancel()
}
