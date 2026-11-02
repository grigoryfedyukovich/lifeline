// Dead constant branches cannot discharge cancellation
package constant_dead_cancel

import (
	"context"
)

func Start(parent context.Context) {
	_, cancel := context.WithCancel(parent)
	if false {
		cancel()
	}
}
