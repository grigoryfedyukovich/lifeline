package coverage_unsupported

func Start(fn func()) {
	go fn()
}
