// Captured cleanup takes effect at invocation, not definition
package closure_cancel_invoked

import (
	"context"
)

func Start(parent context.Context) {
	_, cancel := context.WithCancel(parent)
	release := func() { cancel() }
	release()
}
