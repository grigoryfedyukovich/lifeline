// Imported constructor obligations are attributed to each caller independently
package cross_package_constructor_callers

import (
	"context"
	"github.com/gfedyukovich/lifeline/examples/cross_package_constructor_callers/worker"
)

func Good(parent context.Context) { h := worker.New(parent); h.Cancel() }
func Bad(parent context.Context)  { h := worker.New(parent); _ = h }
