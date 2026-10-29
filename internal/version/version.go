package version

const (
	Tool           = "lifeline"
	Version        = "0.1.2"
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
	//
	// FactVersion 5 replaces FunctionFact.ParamConsumption (a bare "consumed
	// somehow") with ParamEffects (may / must / returns / opaque). The two
	// are not interchangeable: the boolean let a helper that cancels under a
	// condition, one that cancels always and one that merely hands the value
	// on all erase the caller's obligation alike (audit finding F6).
	//
	// FactVersion 6 extends ReturnFieldSite with per-obligation identity and
	// the exporting constructor's final caller-discharge state. Version 5 can
	// describe only a field shape; treating its zero-valued new fields as a
	// real verdict would silently lose cross-package constructor obligations
	// (audit finding F2), so old facts are rejected.
	// Version 7 rejects old facts that credit asynchronous launches as joins.
	// Version 8 removes impossible constant if/for edges from facts.
	// Version 9 tracks factory obligations in local var declarations.
	// Version 10 anchors direct return-expression calls, including return
	// g.Wait(), so effect and all-path summaries include their execution.
	FactVersion = 10
	Backend     = "local-ast-types-ssa-summary/v2"
)
