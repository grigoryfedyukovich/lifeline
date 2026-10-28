package var_cancels

import "context"

func Start(parent context.Context) { var _, cancel = context.WithCancel(parent); _ = cancel }
