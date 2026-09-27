package fieldaliasneverconsumed

import "context"

type worker struct{ cancel context.CancelFunc }

// Start is field_alias_consumed's own negative counterpart: x is created
// as a value alias of w, but neither w nor x ever calls the field back.
// Before collectFieldAliases existed, the `x := w` assignment itself was
// enough to mark w otherUse and silently suppress this leak, the same way
// it happened to (coincidentally, for the same wrong reason) suppress
// nothing when x did call it back.
func Start() {
	ctx, cancel := context.WithCancel(context.Background())
	w := worker{cancel: cancel}
	x := w
	_ = x
	go func() { <-ctx.Done() }()
}
