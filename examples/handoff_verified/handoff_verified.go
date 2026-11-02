// Partial cleanup must retain unsupported ownership-transfer status
package handoff_verified

import (
	"context"
)

func New(parent context.Context, enabled bool) context.CancelFunc {
	_, cancel := context.WithCancel(parent)
	cancel()
	return cancel
}
