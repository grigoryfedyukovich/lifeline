package version

const (
	Tool           = "lifeline"
	Version        = "0.1.1"
	InformationURI = "https://github.com/gfedyukovich/lifeline"
	ReportSchema   = "lifeline.report/v1"
	// FactVersion 3 (Phase 4, docs/cfg-migration-plan.md) added
	// LoopUnresolved: a cross-package fact's own CFG/SCC-derived LL1002
	// verdict, alongside the prior flat booleans. A version-2 fact (or
	// older) does not carry it; ImportObjectFact rejects any fact whose
	// Version does not match exactly, so an older fact is simply
	// unavailable rather than reinterpreted with the new field defaulted
	// to a possibly-wrong value.
	//
	// FactVersion 4 changes what LoopUnresolved means without changing its
	// shape: a call that receives a tracked context (or a configured stop
	// wrapper) no longer models the rest of the function as unreachable,
	// so a version-3 fact may claim a loop was resolved only because an
	// earlier delegation deleted it from the graph (audit finding F1).
	// Rejecting version-3 facts is safer than reinterpreting them.
	FactVersion = 4
	Backend     = "local-ast-types-ssa-summary/v2"
)
