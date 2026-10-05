package closure_local_cancel

import "context"

// Start hands the work to a goroutine. The goroutine creates a derived
// context and never cancels it, so the context stays registered with its
// parent until the parent is cancelled. The cancel function is declared
// inside the literal, so it is the literal's obligation.
func Start(parent context.Context, jobs <-chan int) {
	go func() {
		ctx, cancel := context.WithCancel(parent)
		_ = cancel
		for job := range jobs {
			_ = job
			_ = ctx
		}
	}()
}
