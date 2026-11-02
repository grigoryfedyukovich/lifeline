// Cancellation acquired by a local var declaration
package var_cancel_lost

import (
	"context"
)

func Start(parent context.Context) { var _, cancel = context.WithCancel(parent); _ = cancel }
