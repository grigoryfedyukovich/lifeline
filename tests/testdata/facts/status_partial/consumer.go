package status_partial

import "context"

func New(parent context.Context, enabled bool) context.CancelFunc {
	_, cancel := context.WithCancel(parent)
	if enabled {
		cancel()
	}
	return cancel
}
