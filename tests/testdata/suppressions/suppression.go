package suppressions

import "context"

func valid(parent context.Context) {
	_, cancel := context.WithCancel(parent) //lifeline:ignore LL1001 -- tracked in TICKET-123
	_ = cancel
}

func nearMiss(parent context.Context) {
	_, cancel := context.WithCancel(parent) //nolifeline:ignore LL1001
	_ = cancel
}

func invalidRuleList(parent context.Context) {
	_, cancel := context.WithCancel(parent) //lifeline:ignore LL1001,LL9999
	_ = cancel
}
