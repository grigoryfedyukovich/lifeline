package customcontextwrapper

import "context"

// ProjectWithCancel is a project-specific wrapper around context.WithCancel.
func ProjectWithCancel(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(parent)
}

// Start intentionally discards the wrapper's cancellation function.
func Start(parent context.Context) {
	ctx, _ := ProjectWithCancel(parent)
	go run(ctx)
}

func run(ctx context.Context) {
	<-ctx.Done()
}
