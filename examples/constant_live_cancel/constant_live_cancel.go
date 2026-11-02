// Dead constant branches cannot discharge cancellation
package constant_live_cancel

import (
	"context"
)

func Start(parent context.Context) {
	_, cancel := context.WithCancel(parent)
	if true {
		cancel()
	}
}
