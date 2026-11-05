package vet_include_tests

import "context"

func lostCancelInTest(parent context.Context) {
	_, cancel := context.WithCancel(parent)
	_ = cancel
}
