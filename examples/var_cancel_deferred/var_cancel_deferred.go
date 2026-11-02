// Cancellation acquired by a local var declaration
package var_cancel_deferred

import (
	"context"
)

func Start(parent context.Context) { var _, cancel = context.WithCancel(parent); defer cancel() }
