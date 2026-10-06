package status_unsupported_only

import "context"

var keep context.CancelFunc

// The cancel function is stored in a package variable: the obligation is
// handed off to code this analysis does not follow. Nothing is reported, but
// the run is not equivalent to one that verified the cancel call.
func Start(parent context.Context) {
	_, cancel := context.WithCancel(parent)
	keep = cancel
}
