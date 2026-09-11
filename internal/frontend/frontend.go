package frontend

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	flowgraph "github.com/gfedyukovich/lifeline/internal/cfg"
	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/localssa"
	"github.com/gfedyukovich/lifeline/internal/model"
)

type Input struct {
	Fset  *token.FileSet
	Files []*ast.File
	Pkg   *types.Package
	Info  *types.Info

	// LookupFunctionSummary provides a version-validated lifecycle summary for
	// a direct function target whose source body is outside the current package.
	// The go/analysis adapter backs this with versioned object facts. Standalone
	// mode leaves it nil and reports such targets as unsupported.
	LookupFunctionSummary func(*types.Func) (model.Goroutine, bool)

	// LookupParamConsumption is LookupFunctionSummary's counterpart for
	// argumentConsumed's question -- "does fn's own body consume the
	// parameter at this position" -- for a callee whose source lies
	// outside the current package, backed the same way by a versioned
	// object fact (FunctionFact.ParamConsumption). A same-package callee
	// never uses this: b.paramConsumption already answers it directly,
	// with the added benefit of the interprocedural fixed point (a fact
	// is a one-shot snapshot from whenever the callee's own package was
	// last analyzed, not something this build's own sweep can improve on
	// by trying again). Standalone mode leaves this nil, same as
	// LookupFunctionSummary, and argumentConsumed falls back to "assume
	// transferred" exactly as it does for any other unresolvable callee.
	LookupParamConsumption func(fn *types.Func, paramIndex int) (consumed, ok bool)

	// LookupParamDoneCalled is calleeDoneParamMatches's cross-package
	// counterpart, backed the same way by a versioned fact
	// (FunctionFact.ParamDoneCalled): does fn's own body eventually call
	// Done() on the sync.WaitGroup parameter at this position, to any
	// depth. Standalone mode leaves this nil, same as the other two
	// Lookup* hooks, and calleeDoneParamMatches simply reports false for
	// a cross-package callee then, same as it always did before this
	// existed.
	LookupParamDoneCalled func(fn *types.Func, paramIndex int) (called, ok bool)

	// LookupReturnFieldSites is the constructor/field-ownership
	// counterpart of LookupParamConsumption and LookupParamDoneCalled:
	// for a callee outside the current package, describes which of its
	// result positions carry a cancel-like or group-like value inside a
	// named struct field (FunctionFact.ReturnFieldSites). Consulted by
	// computeConstructorCallerConsumption when a call's callee isn't
	// found among this package's own b.returnFieldInfo.
	//
	// Unlike the other two Lookup* hooks, a positive result here cannot
	// ever change the constructor's own diagnostic the way a same-package
	// verification does: that verdict is recorded into
	// b.returnFieldConsumption, keyed by the constructor's own local
	// binding object -- which exists only within the same build as the
	// constructor's own source, i.e. only for a same-package constructor.
	// A cross-package constructor's package has already been analyzed and
	// its diagnostics already finalized by the time this package (a
	// dependent) is analyzed; go/analysis facts flow from a dependency to
	// its dependents, never backward, so there is no way for a finding
	// made here to reach back and revise that already-reported verdict.
	// This hook still lets computeConstructorCallerConsumption run the
	// same verification against the *caller's* own body (mechanically
	// removing verifyConstructorCallerField's dependency on a real,
	// same-package types.Object to distinguish cancel from
	// waitgroup/errgroup, which used to make this simply unreachable for
	// a cross-package callee) -- its result is recorded the same way a
	// same-package verdict is, keyed by the callee's own *types.Func
	// (there being no local binding object to key it by instead), so it
	// remains available to any future consumer that queries it directly,
	// without inventing a new diagnostic location for it today.
	LookupReturnFieldSites func(fn *types.Func) ([]model.ReturnFieldSite, bool)
}

type funcSource struct {
	decl *ast.FuncDecl
	obj  *types.Func
}

type builder struct {
	in        Input
	cfg       config.Config
	funcs     map[*types.Func]*ast.FuncDecl
	analyzed  map[*types.Func]bool
	summaries map[*types.Func]model.Goroutine
	// paramConsumption records, for a cancel-like or group-like function
	// parameter's own *types.Var object, whether that function's own body
	// consumes it (calls it, or further transfers it) -- Phase 5 of the
	// AST->CFG migration (docs/cfg-migration-plan.md), "direct parameter
	// passing", extended to multiple hops by a small interprocedural fixed
	// point: Build calls computeParameterConsumption for every function
	// repeatedly, not once, until no entry changes. A missing map entry
	// means "not computed yet, possibly still converging", never "verified
	// not consumed" -- see argumentConsumed's pending return value, which
	// is what lets a chain resolve correctly regardless of which order
	// functions are declared in, instead of only as far as a single
	// declaration-order pass happens to have already reached a deeper
	// callee. Once Build's fixed-point loop finishes, every lookup here is
	// a pure, stable map read; argumentConsumed itself never triggers a
	// fresh computation, which is what keeps it non-recursive.
	paramConsumption map[types.Object]bool
	// paramDoneCalled records, for a sync.WaitGroup-typed parameter,
	// whether calleeDoneParamMatches's own fixed point (computeParamDoneCalled)
	// has established that Done() is eventually called on it -- directly,
	// or via any number of further resolvable same-package functions it
	// gets passed on to as a direct argument. Unlike paramConsumption,
	// this needs no "pending" signal alongside it: a not-yet-true entry
	// mid-sweep is exactly the same safe default ("no evidence yet") this
	// analysis already treats it as everywhere else, and simply gets
	// corrected upward on a later sweep if warranted -- there is no
	// action taken on a false reading here that a later true reading
	// would need to retroactively undo, the way "assume transferred"
	// would for paramConsumption.
	paramDoneCalled map[types.Object]bool
	// inParamPrepass is true only while computeParameterConsumption's own
	// call into observeFunctionBody is on the stack. It tells observeCall
	// to treat a "pending" dependency (argumentConsumed's third return
	// value: a real, resolvable same-package callee whose own summary
	// merely hasn't been computed by this iteration yet) as "no answer
	// yet, try again next sweep" rather than falling back to "assume
	// transferred" -- that fallback is correct once and for all for a
	// callee that can never be resolved (a different package, an
	// interface method, a function value, a `...` spread), but would
	// wrongly freeze a same-package chain's result at whatever the first
	// sweep happened to see, reintroducing the exact declaration-order
	// dependence the fixed point exists to remove.
	inParamPrepass bool
	// singleAssignTargets records, for the function body currently being
	// walked by observeFunctionBody, every local variable or parameter
	// assigned exactly once in that body, mapped to that one assignment's
	// right-hand side expression -- see singleAssignmentTargets for why
	// "exactly once" is what makes this sound. Set fresh at the top of
	// every observeFunctionBody call (both buildFunction's main pass and
	// computeParameterConsumption's scratch pre-pass; observeFunctionBody
	// is never called recursively for a nested function literal, so
	// there is no reentrancy to guard against here the way
	// inParamPrepass needs to). argumentConsumed's resolveCalleeFunc and
	// compositeLitOf are the only two readers.
	singleAssignTargets map[types.Object]ast.Expr
	// returnFieldInfo records, for a cancel/group binding's own local
	// identifier object (a cancelState.cancelObj or groupState.obj), the
	// single narrow shape this file tracks for "constructor-returned
	// objects" (docs/roadmap.md item 3): that binding is stored into a
	// named struct field which is itself returned by the function that
	// declared the binding, either directly inline in the return
	// statement or via a local variable assigned the struct literal and
	// then returned unconsumed. Populated by computeFieldOwnership's
	// pre-pass (run for every function before any caller-side check),
	// consumed by computeConstructorCallerConsumption to know which
	// functions are "constructors" worth checking callers of, and by
	// recordReturnedField's own lookup into returnFieldConsumption below
	// once every function's callers have been checked.
	returnFieldInfo map[types.Object]returnFieldSite
	// returnFieldConsumption records, for the same binding-object keys as
	// returnFieldInfo, whether a resolvable direct (same-package,
	// statically-called) caller of the owning constructor was found to
	// read the returned struct's field back and consume it --
	// true: at least one such caller does; false: at least one such
	// caller was checked and confidently does not, and none does;
	// absent: no caller could be checked with confidence either way, or
	// the constructor is never called within this package's analyzed
	// bound. Absent is treated exactly like true (the conservative
	// assume-transferred default used throughout this file whenever a
	// value's fate can't be verified) -- only an explicit false, from
	// positive evidence at every checked call site, ever produces a
	// finding. Populated by computeConstructorCallerConsumption.
	returnFieldConsumption map[types.Object]bool
	contextInterface       *types.Interface
	contextFactories       map[string]struct{}
	startWrappers          map[string]struct{}
	joinWrappers           map[string]struct{}
	stopWrappers           map[string]struct{}
}

// returnFieldSite is the shape computeFieldOwnership records into
// b.returnFieldInfo: which field of which function's own return value
// carries a specific cancel/group binding out of the function that
// created it, for computeConstructorCallerConsumption to verify against
// that function's own direct callers.
type returnFieldSite struct {
	fieldName   string
	resultIndex int
	fn          *types.Func
}

// fieldCapture records a lifecycle binding (a cancel function or a join
// group) stored into a specific named field of a struct value, either
// held by a single local variable (varObj set, returnIndex -1) or
// constructed directly inline in a return statement (varObj nil,
// returnIndex the result position) -- the two shapes collectFieldCaptures
// recognizes for "selected struct fields" (docs/roadmap.md item 3). This
// is a narrow, identity-tracked alternative to the unconditional "stored
// in a composite value => escaped" fallback (observeContainerEscape):
// instead of assuming the obligation was discharged the instant it is
// stored, resolveFieldCaptures verifies whether the same variable's field
// is later read back and consumed (a call through the field for a cancel
// function, or a Wait/Add/Go call through the field for a group) before
// falling back to the prior conservative assume-transferred behavior for
// anything more indirect than that. It deliberately does not follow an
// arbitrary alias graph: only this one specific shape (a single named
// local variable, or a direct return) is tracked, exactly as
// docs/limitations.md's "aliasing is shallow" boundary describes for
// every other escape form in this file.
type fieldCapture struct {
	varObj      types.Object
	fieldName   string
	cancel      *cancelState
	group       *groupState
	returnIndex int
}

// generatedFilePattern matches the standard Go convention for marking a
// source file as generated: https://go.dev/s/generatedcode. Tools that
// modify or lint source are expected to recognize and skip such files.
var generatedFilePattern = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// IsGeneratedFile reports whether file carries the standard generated-code
// marker comment. It checks parsed comments rather than raw source text, so
// it works from an already-parsed *ast.File without re-reading the file.
func IsGeneratedFile(file *ast.File) bool {
	if file == nil {
		return false
	}
	for _, group := range file.Comments {
		for _, c := range group.List {
			if generatedFilePattern.MatchString(strings.TrimSpace(c.Text)) {
				return true
			}
		}
	}
	return false
}

// FilterFiles narrows files to those that should actually be walked for
// lifecycle constructs: it excludes files matching cfg.IgnorePaths (matched
// against the file's path relative to cwd, falling back to the absolute
// path if cwd is empty or unrelated) and files carrying the standard
// generated-code marker (see IsGeneratedFile). This is meant to run after
// type-checking, not before: type-checking should still see every file in
// the package, since excluding a file there could break resolution of
// symbols other files in the same package legitimately depend on. Only the
// lifecycle analysis pass itself skips them.
func FilterFiles(fset *token.FileSet, files []*ast.File, cfg config.Config, cwd string) []*ast.File {
	out := files[:0:0]
	for _, f := range files {
		if IsGeneratedFile(f) {
			continue
		}
		if len(cfg.IgnorePaths) > 0 {
			path := fset.Position(f.Pos()).Filename
			rel := path
			if cwd != "" {
				if r, err := filepath.Rel(cwd, path); err == nil {
					rel = r
				}
			}
			if cfg.MatchesIgnorePath(rel) {
				continue
			}
		}
		out = append(out, f)
	}
	return out
}

// suppressionPattern matches an inline suppression directive within a
// comment, e.g. "//lifeline:ignore" (suppresses every rule reported on that
// line) or "//lifeline:ignore LL1001,LL1002" (suppresses only those rules).
// Free text may follow, e.g. "//lifeline:ignore LL1002 -- see TICKET-123".
var suppressionPattern = regexp.MustCompile(`lifeline:ignore(?:\s+([A-Za-z0-9,]+))?`)

// collectSuppressions scans every comment in files for a suppression
// directive and returns a file -> line -> rule-IDs index. A bare directive
// with no rule list is recorded as "*", meaning every rule is suppressed on
// that line. The directive is matched by the comment's own line, so it is
// expected on the same source line as the construct it applies to (the
// convention golangci-lint's "//nolint" uses), not the line before it.
func collectSuppressions(fset *token.FileSet, files []*ast.File) map[string]map[int][]string {
	out := map[string]map[int][]string{}
	for _, file := range files {
		for _, group := range file.Comments {
			for _, c := range group.List {
				m := suppressionPattern.FindStringSubmatch(c.Text)
				if m == nil {
					continue
				}
				pos := fset.Position(c.Pos())
				byLine := out[pos.Filename]
				if byLine == nil {
					byLine = map[int][]string{}
					out[pos.Filename] = byLine
				}
				if m[1] == "" {
					byLine[pos.Line] = append(byLine[pos.Line], "*")
					continue
				}
				for _, id := range strings.Split(m[1], ",") {
					id = strings.ToUpper(strings.TrimSpace(id))
					if id != "" {
						byLine[pos.Line] = append(byLine[pos.Line], id)
					}
				}
			}
		}
	}
	return out
}

func Build(in Input, cfg config.Config) (model.Program, error) {
	if in.Fset == nil || in.Pkg == nil || in.Info == nil {
		return model.Program{}, fmt.Errorf("frontend requires file set, package, and type information")
	}
	b := &builder{
		in:                     in,
		cfg:                    cfg,
		funcs:                  map[*types.Func]*ast.FuncDecl{},
		analyzed:               map[*types.Func]bool{},
		summaries:              map[*types.Func]model.Goroutine{},
		paramConsumption:       map[types.Object]bool{},
		paramDoneCalled:        map[types.Object]bool{},
		returnFieldInfo:        map[types.Object]returnFieldSite{},
		returnFieldConsumption: map[types.Object]bool{},
		contextInterface:       findContextInterface(in.Pkg),
		contextFactories:       stringSet(cfg.ContextWrappers),
		startWrappers:          stringSet(cfg.StartWrappers),
		joinWrappers:           stringSet(cfg.JoinWrappers),
		stopWrappers:           stringSet(cfg.StopWrappers),
	}
	var sources []funcSource
	for _, file := range in.Files {
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			obj, _ := in.Info.Defs[fd.Name].(*types.Func)
			if obj != nil {
				b.funcs[obj] = fd
			}
			sources = append(sources, funcSource{decl: fd, obj: obj})
		}
	}

	program := model.Program{PackagePath: in.Pkg.Path(), FunctionCount: len(sources), Suppressions: collectSuppressions(in.Fset, in.Files)}
	limit := len(sources)
	if limit > cfg.MaxFunctions {
		limit = cfg.MaxFunctions
		program.Truncated = true
	}
	for _, source := range sources[:limit] {
		if source.obj != nil {
			b.analyzed[source.obj] = true
		}
	}
	// Pre-pass (Phase 5, docs/cfg-migration-plan.md): compute which
	// cancel-like/group-like parameters each function's own body consumes,
	// before any buildFunction call cross-references another function's
	// result. This must run as its own pass, not be folded into
	// buildFunction's main loop below: a caller earlier in file order than
	// its callee would otherwise see an empty result for that callee
	// purely due to processing order, not because the callee is genuinely
	// unanalyzable.
	//
	// A single sweep over sources[:limit] is not enough on its own: a
	// chain like A(c){B(c)}, B(c){C(c)}, C(c){/* consume */} needs C's
	// result before B's can be computed, and B's before A's, so one pass
	// only resolves as deep as declaration order happens to already
	// support -- the exact "different analysis result purely from source
	// order" problem this loop exists to remove. So this keeps re-running
	// computeParameterConsumption for every function, in the same order
	// each time, until a full sweep changes nothing: each sweep can only
	// ever add information (a param's own recorded value can go from
	// absent to known, or from known-false to known-true, never back --
	// see argumentConsumed's "pending" case and inParamPrepass), so this
	// is a small monotone fixed point over a lattice with two possible
	// rises per parameter, guaranteeing termination in at most that many
	// sweeps, and it converges to the same result regardless of which
	// order the functions happen to be declared in. See argumentConsumed
	// for how a lookup degrades safely (falls back to the prior
	// unconditional-escape behavior) once the fixed point is reached and a
	// callee still has no entry at all -- max_functions truncation being
	// the main reason that would happen.
	//
	// computeParamDoneCalled runs in the same sweep, for the same
	// structural reason (a named-worker's own "does it delegate to Done()
	// eventually" answer can depend on a further helper's not-yet-computed
	// one): it answers calleeDoneParamMatches's question -- does a
	// sync.WaitGroup parameter eventually get Done() called on it,
	// directly or via any number of further resolvable same-package
	// functions -- which is independent of paramConsumption's own
	// question and does not need its own separate loop.
	for {
		changed := false
		for _, source := range sources[:limit] {
			if b.computeParameterConsumption(source.decl) {
				changed = true
			}
			if b.computeParamDoneCalled(source.decl) {
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	// Two further pre-passes, run in this order and each over every
	// function before the next begins, behind the "constructor-returned
	// objects" half of field/constructor ownership tracking
	// (docs/roadmap.md item 3) -- the same "isolated pre-pass, not folded
	// into buildFunction's main loop" reasoning computeParameterConsumption
	// above already documents, extended one step further: a constructor's
	// own returned-field shape (computeFieldOwnership) must be known
	// before any of its callers can be checked (computeConstructorCallerConsumption),
	// regardless of which one is declared first in source order, and every
	// caller must in turn be checked before buildFunction's real pass
	// below looks up the answer.
	for _, source := range sources[:limit] {
		b.computeFieldOwnership(source)
	}
	for _, source := range sources[:limit] {
		b.computeConstructorCallerConsumption(source)
	}
	for _, source := range sources[:limit] {
		program.Functions = append(program.Functions, b.buildFunction(source))
	}
	return program, nil
}

type cancelState struct {
	binding   model.CancelBinding
	ctxObj    types.Object
	cancelObj types.Object
	// callSites records every direct call to cancelObj itself (the "cancel-
	// call" evidence case in observeCall), in AST-node identity form rather
	// than just the span already on Evidence. computeGroupOrdering
	// (docs/cfg-migration-plan.md, Phase 3 completion) uses this to find
	// which CFG block a candidate "stop signal" call landed in, via
	// Build's call-site map -- a lookup that needs the exact node, not a
	// span, since a defer-wrapped call's own span differs from its
	// underlying *ast.CallExpr's.
	callSites []*ast.CallExpr
	// pendingAliasEscapes records every other cancel binding this one's
	// cancel function was assigned into (`other := cancel` or `other =
	// cancel`), where that other identifier is itself a separately
	// tracked cancel binding rather than some arbitrary, untracked
	// variable. See resolveAliasEscapes for why this is deferred rather
	// than decided immediately in observeEscapeAssignment.
	pendingAliasEscapes []*cancelState
}

type groupState struct {
	group model.JoinGroup
	obj   types.Object
	// waitCallSites is the same kind of AST-node record as
	// cancelState.callSites above, for the same reason: finding each
	// Wait() call's CFG block by identity, not by span.
	waitCallSites []*ast.CallExpr
	// pendingAliasEscapes is groupState's analogue of
	// cancelState.pendingAliasEscapes above, for `other := &wg` / `other
	// = &wg` where other is itself a separately tracked group binding.
	pendingAliasEscapes []*groupState
}

func (b *builder) buildFunction(source funcSource) model.Function {
	fd := source.decl
	name := fd.Name.Name
	if source.obj != nil {
		name = source.obj.FullName()
	}
	fn := model.Function{Name: name, Span: b.span(fd)}
	fn.ParamConsumption = b.exportedParamConsumption(fd)
	fn.ParamDoneCalled = b.exportedParamDoneCalled(fd)
	fn.ReturnFieldSites = b.exportedReturnFieldSites(source.obj)
	contexts := map[types.Object]string{}
	b.collectContextParams(fd.Type, contexts)
	states, groups := b.collectBindings(fd, contexts)

	fn.BodyLifecycle = b.newLifecycleSummary(fd.Body, contexts, "function-body", b.span(fd.Body), false)
	fn.BodyLifecycle.CFG, _ = flowgraph.Build(name, b.in.Fset, fd.Body, b.in.Info, b.trustedTerminator(contexts))
	b.observeFunctionBody(fd.Body, contexts, states, groups, &fn, source.obj)
	resolveAliasEscapes(states, groups)
	b.computeGroupBalances(groups, fd.Body, b.in.Info)
	// computeGroupOrdering needs real control-flow reachability, not
	// fn.BodyLifecycle.CFG's own trusted-stop edges: those model "a call
	// receiving a tracked context is trusted to eventually terminate",
	// calibrated for LL1002's loop-escape question, where treating such a
	// call as if it reached the function's exit is a reasonable
	// abstraction. It is not a reasonable one here -- context.WithCancel
	// obviously returns normally, and the extremely common `ctx, cancel :=
	// context.WithCancel(parent)` idiom would otherwise make everything
	// after it look unreachable from entry, which is never actually true.
	// So this builds its own, separate, purely structural CFG (nil trust
	// predicate) rather than reusing fn.BodyLifecycle.CFG, at the cost of
	// building the CFG twice per function.
	orderingCFG, orderingCallBlocks := flowgraph.Build(name, b.in.Fset, fd.Body, b.in.Info, nil)
	b.computeGroupOrdering(groups, states, fd.Body, orderingCFG, orderingCallBlocks)
	if source.obj != nil {
		b.summaries[source.obj] = cloneGoroutine(fn.BodyLifecycle)
	}

	for _, name := range contexts {
		fn.Contexts = append(fn.Contexts, name)
	}
	sort.Strings(fn.Contexts)
	for _, s := range states {
		fn.Cancels = append(fn.Cancels, s.binding)
	}
	for _, g := range groups {
		fn.Groups = append(fn.Groups, g.group)
	}
	sort.Slice(fn.Cancels, func(i, j int) bool { return fn.Cancels[i].Span.StartOffset < fn.Cancels[j].Span.StartOffset })
	sort.Slice(fn.Groups, func(i, j int) bool { return fn.Groups[i].Span.StartOffset < fn.Groups[j].Span.StartOffset })

	ir := localssa.Build(name, fd.Body, b.in.Info)
	fn.IR = make([]model.Instruction, 0, len(ir.Instructions))
	for _, in := range ir.Instructions {
		fn.IR = append(fn.IR, model.Instruction{
			Index: in.Index, Op: string(in.Op), Span: spanPositions(b.in.Fset, in.Pos, in.End),
			Callee: in.Callee, Defines: append([]string(nil), in.Defines...), Uses: append([]string(nil), in.Uses...),
		})
	}
	return fn
}

func (b *builder) collectContextParams(ft *ast.FuncType, contexts map[types.Object]string) {
	if ft == nil || ft.Params == nil {
		return
	}
	for _, field := range ft.Params.List {
		for _, id := range field.Names {
			obj := b.in.Info.Defs[id]
			if obj != nil && isContextType(obj.Type(), b.contextInterface) {
				contexts[obj] = id.Name
			}
		}
	}
}

// collectBindings combines cancellation and join-group definition discovery in
// one traversal. Nested function literals have independent locals and are not
// folded into the enclosing function's ownership model.
// isCancelFuncType reports whether t is context.CancelFunc, context's
// related CancelCauseFunc, or a plausible stand-in for one: any function
// type with no results and at most one parameter. The permissive fallback
// exists because a configured context_wrapper is not required to use the
// named context types, only to behave like context.WithCancel: return a
// context alongside a callable that ends it.
func isCancelFuncType(t types.Type) bool {
	if t == nil {
		return false
	}
	if named, ok := t.(*types.Named); ok {
		if obj := named.Obj(); obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == "context" &&
			(obj.Name() == "CancelFunc" || obj.Name() == "CancelCauseFunc") {
			return true
		}
	}
	sig, ok := t.Underlying().(*types.Signature)
	return ok && sig.Results().Len() == 0 && sig.Params().Len() <= 1
}

// contextFactoryRoles finds which results of a context-factory call are the
// context and the cancel function by their static types rather than by
// position. context.WithCancel's own signature happens to return them in
// (context, cancel) order, which is also the near-universal convention for
// wrapper functions that mimic it, but nothing here assumes that order: a
// wrapper is free to return them in either order, and free to return
// additional results (e.g. a trailing error) as long as exactly one result
// is context-typed and exactly one other result looks like a cancel
// function. Returns -1, -1 if the roles can't be identified with
// confidence; collectBindings treats that as "not a recognized factory
// shape" and does not guess.
func contextFactoryRoles(callType types.Type, contextInterface *types.Interface) (ctxIdx, cancelIdx int) {
	ctxIdx, cancelIdx = -1, -1
	tuple, ok := callType.(*types.Tuple)
	if !ok {
		return
	}
	for i := 0; i < tuple.Len(); i++ {
		if isContextType(tuple.At(i).Type(), contextInterface) {
			ctxIdx = i
			break
		}
	}
	if ctxIdx == -1 {
		return
	}
	if tuple.Len() == 2 {
		cancelIdx = 1 - ctxIdx
		return
	}
	for i := 0; i < tuple.Len(); i++ {
		if i == ctxIdx {
			continue
		}
		if isCancelFuncType(tuple.At(i).Type()) {
			cancelIdx = i
			return
		}
	}
	return
}

func (b *builder) collectBindings(fd *ast.FuncDecl, contexts map[types.Object]string) ([]*cancelState, []*groupState) {
	var states []*cancelState
	var groups []*groupState
	seenGroups := map[types.Object]bool{}
	var allNames map[string]bool // computed only for the rare blank-cancel fix

	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		switch x := n.(type) {
		case *ast.AssignStmt:
			if len(x.Rhs) != 1 || len(x.Lhs) < 2 {
				break
			}
			call, ok := x.Rhs[0].(*ast.CallExpr)
			if !ok {
				break
			}
			factory := b.callName(call)
			if !b.isContextFactory(factory) {
				break
			}
			ctxIdx, cancelIdx := contextFactoryRoles(b.in.Info.TypeOf(call), b.contextInterface)
			if ctxIdx == -1 || cancelIdx == -1 || ctxIdx >= len(x.Lhs) || cancelIdx >= len(x.Lhs) {
				break // roles could not be identified by type; do not guess positionally
			}
			ctxID, ctxOK := x.Lhs[ctxIdx].(*ast.Ident)
			cancelID, cancelOK := x.Lhs[cancelIdx].(*ast.Ident)
			if !ctxOK || !cancelOK {
				break // field/container ownership is explicit and not guessed
			}
			state := &cancelState{binding: model.CancelBinding{Factory: factory, Span: b.span(x)}}
			if ctxID.Name != "_" {
				state.ctxObj = b.in.Info.ObjectOf(ctxID)
				state.binding.ContextName = ctxID.Name
				if state.ctxObj != nil {
					contexts[state.ctxObj] = ctxID.Name
				}
			}
			if cancelID.Name == "_" {
				state.binding.Discarded = true
				if x.Tok == token.DEFINE {
					if allNames == nil {
						allNames = identifierNames(fd)
					}
					name := uniqueName("lifelineCancel", allNames)
					state.binding.SuggestedFix = &model.SuggestedFix{
						Message: "retain and defer the cancellation function",
						Edits: []model.FixEdit{
							{Span: b.span(cancelID), NewText: name},
							{Span: zeroWidthAtEnd(b.span(x)), NewText: "; defer " + name + "()"},
						},
					}
				}
				state.binding.Evidence = append(state.binding.Evidence, model.Evidence{Kind: "discard", Message: "the cancellation result is assigned to the blank identifier", Span: ptrSpan(b.span(cancelID))})
			} else {
				state.cancelObj = b.in.Info.ObjectOf(cancelID)
				state.binding.CancelName = cancelID.Name
				if isNamedResult(fd.Type, state.cancelObj, b.in.Info) {
					state.binding.Escapes = true
					state.binding.Evidence = append(state.binding.Evidence, model.Evidence{Kind: "return-ownership", Message: "cancellation function is returned to the caller", Span: ptrSpan(b.span(cancelID))})
				}
			}
			states = append(states, state)
		case *ast.Ident:
			if x.Name == "_" {
				break
			}
			obj := b.in.Info.Defs[x]
			if obj == nil || seenGroups[obj] {
				break
			}
			kind := groupKind(obj.Type())
			if kind == "" {
				break
			}
			seenGroups[obj] = true
			groups = append(groups, &groupState{obj: obj, group: model.JoinGroup{Kind: kind, Name: x.Name, Span: b.span(x)}})
		}
		return true
	})
	return states, groups
}

func (b *builder) observeFunctionBody(body *ast.BlockStmt, contexts map[types.Object]string, cancels []*cancelState, groups []*groupState, fn *model.Function, fnObj *types.Func) {
	b.singleAssignTargets = singleAssignmentTargets(body, b.in.Info)
	// Field/constructor ownership tracking (docs/roadmap.md item 3): find
	// every "stored struct" or "constructor" field-capture shape in body
	// up front, before the main traversal below runs, so the generic
	// unconditional composite-literal/return escape fallbacks
	// (observeContainerEscape, observeReturn) know via claimed which
	// specific bindings not to mark Escapes=true for -- their fate is
	// instead settled by resolveFieldCaptures at the end of this
	// function, once the whole body has been seen.
	captures, claimed := b.collectFieldCaptures(body, cancels, groups)
	// Same reasoning, for the same reason, for the direct-parameter-
	// passing spread case (argumentIndexOf/compositeLitOf): a cancel
	// placed in a composite literal that is itself spread into some
	// call's variadic argument is also, unavoidably, "stored in a
	// composite literal" as far as observeContainerEscape's generic walk
	// is concerned, and would otherwise be marked transferred before
	// observeCall's own, more precise argumentConsumed check ever gets a
	// say -- this claim just defers to that more precise check once the
	// call node is actually visited, below; it does not decide the
	// verdict itself.
	collectSpreadClaims(body, cancels, b.in.Info, b.singleAssignTargets, claimed)
	// Same claimed mechanism again, this time for the slice/map half of
	// "storing a cancel/group value into an arbitrary container is still
	// treated as an unverified ownership transfer" (docs/limitations.md):
	// a cancel value placed into a slice or map literal that is itself
	// assigned, exactly once, to a local variable later ranged over and
	// called is verified directly by collectContainerCaptures, which also
	// claims it so observeContainerEscape's generic fallback doesn't
	// pre-empt that verdict with an unconditional "transferred" the
	// moment the literal is seen.
	b.collectContainerCaptures(body, cancels, b.in.Info, b.singleAssignTargets, claimed)
	// The same traversal observes lifecycle uses and goroutine start sites. A
	// small depth stack keeps the enclosing function's own termination summary
	// from inheriting loops or exits from nested function literals.
	funcDepth := 0
	var nodeIsFuncLit []bool
	labels := labeledLoops(body)
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			last := len(nodeIsFuncLit) - 1
			if last >= 0 {
				if nodeIsFuncLit[last] {
					funcDepth--
				}
				nodeIsFuncLit = nodeIsFuncLit[:last]
			}
			return true
		}
		_, isFuncLit := n.(*ast.FuncLit)
		nodeIsFuncLit = append(nodeIsFuncLit, isFuncLit)
		if isFuncLit {
			funcDepth++
		}
		if funcDepth == 0 {
			b.observeLifecycleNode(n, contexts, labels, &fn.BodyLifecycle)
		}
		switch x := n.(type) {
		case *ast.CallExpr:
			b.observeCall(x, cancels, groups)
			if b.isConfigured(x, b.startWrappers) {
				if target := firstStartTarget(x.Args, b.in.Info); target != nil {
					g := b.buildGoroutine(x, &ast.CallExpr{Fun: target}, contexts, "configured-start")
					fn.Goroutines = append(fn.Goroutines, g)
					b.markChildUses(x, cancels)
				}
			}
		case *ast.ReturnStmt:
			b.observeReturn(x, cancels, groups, claimed)
		case *ast.AssignStmt:
			b.observeEscapeAssignment(x, cancels, groups)
		case *ast.CompositeLit:
			b.observeContainerEscape(x, cancels, groups, claimed)
		case *ast.ValueSpec:
			b.observeContainerEscape(x, cancels, groups, claimed)
		case *ast.GoStmt:
			g := b.buildGoroutine(x, x.Call, contexts, "go")
			fn.Goroutines = append(fn.Goroutines, g)
			b.markChildUses(x.Call, cancels)
		}
		return true
	})
	b.resolveFieldCaptures(fnObj, body, captures)
	b.singleAssignTargets = nil
}

// computeParameterConsumption (re)computes, for every cancel-like or
// group-like parameter of fd, whether fd's own body consumes it, by
// running the same Called/Escapes detection machinery used for
// locally-declared cancel and group bindings (observeFunctionBody)
// against fd's own body, with those parameters standing in for what
// would otherwise be locally-declared bindings. This is Phase 5's
// "direct parameter passing" support (docs/cfg-migration-plan.md): it
// lets argumentConsumed later tell whether a cancel/group value passed
// as an argument is actually consumed by the callee, rather than
// unconditionally trusting that passing it anywhere discharges the
// caller's own obligation.
//
// Build calls this once per function per sweep, as part of a
// fixed-point loop, not once ever: fd's own body may itself pass a
// parameter on to another function whose result isn't known yet on an
// early sweep, in which case that call contributes nothing this time
// (see inParamPrepass and argumentConsumed's pending result) and fd's
// recorded value may still rise on a later sweep once that dependency
// resolves. Recording is monotone -- merged with whatever was already
// there, via OR, never overwritten with a lower value -- both because
// that is the only direction new information moves in this analysis
// (something already shown to be consumed stays consumed) and because
// it is what makes the fixed point well-defined regardless of how many
// times or in what order this runs. reportChanged is true iff this call
// caused any entry to change (including a first-time recording of
// "false"; recording that a callee's result is known, even if the
// answer is "not consumed", is itself new information a caller depends
// on -- see argumentConsumed's pending case), which is what tells
// Build's loop whether another sweep is needed.
//
// The resulting model.Function is discarded: this call exists only for
// its side effect on b.paramConsumption, not to produce a second copy of
// fd's own diagnostics (buildFunction does that, separately, for fd's own
// locally-declared bindings, once the fixed point above has fully
// converged).
func (b *builder) computeParameterConsumption(fd *ast.FuncDecl) (reportChanged bool) {
	if fd.Type.Params == nil {
		return false
	}
	var paramCancels []*cancelState
	var paramGroups []*groupState
	for _, field := range fd.Type.Params.List {
		for _, name := range field.Names {
			obj := b.in.Info.ObjectOf(name)
			if obj == nil || name.Name == "_" {
				continue
			}
			if isCancelFuncType(obj.Type()) {
				paramCancels = append(paramCancels, &cancelState{cancelObj: obj, binding: model.CancelBinding{CancelName: name.Name}})
				continue
			}
			if kind := groupKind(obj.Type()); kind != "" {
				paramGroups = append(paramGroups, &groupState{obj: obj, group: model.JoinGroup{Kind: kind, Name: name.Name}})
			}
		}
	}
	// variadicCancelParam is the one remaining shape this checks: a
	// trailing `...context.CancelFunc`-like parameter, whose element type
	// -- not the parameter itself -- is cancel-like. This is what a `...`
	// spread call ultimately lands on (see argumentIndexOf and
	// compositeLitOf): `dropAll([]context.CancelFunc{cancel}...)` calling
	// `func dropAll(cancels ...context.CancelFunc)`. Its own Called/
	// Escapes machinery doesn't apply -- there's no single scalar value
	// to track -- so this is checked separately, below, via
	// variadicCancelElementCalled. Deliberately scoped to only the actual
	// trailing variadic parameter (never a same-shaped plain, non-`...`
	// slice parameter elsewhere in the list): a `...` spread can only
	// ever target that one position, so tracking any other slice-typed
	// parameter here would answer a question argumentIndexOf never asks.
	var variadicCancelParam types.Object
	if params := fd.Type.Params.List; len(params) > 0 {
		last := params[len(params)-1]
		if _, ok := last.Type.(*ast.Ellipsis); ok && len(last.Names) > 0 {
			name := last.Names[len(last.Names)-1]
			if obj := b.in.Info.ObjectOf(name); obj != nil && name.Name != "_" {
				if slice, ok := obj.Type().(*types.Slice); ok && isCancelFuncType(slice.Elem()) {
					variadicCancelParam = obj
				}
			}
		}
	}
	if len(paramCancels) == 0 && len(paramGroups) == 0 && variadicCancelParam == nil {
		return false
	}
	if variadicCancelParam != nil {
		next := variadicCancelElementCalled(fd.Body, variadicCancelParam, b.in.Info)
		if b.recordParamConsumption(variadicCancelParam, next) {
			reportChanged = true
		}
	}
	if len(paramCancels) == 0 && len(paramGroups) == 0 {
		return reportChanged
	}
	contexts := map[types.Object]string{}
	b.collectContextParams(fd.Type, contexts)
	var scratch model.Function
	// fnObj is nil here: this scratch run exists only for its side effect
	// on b.paramConsumption (see the doc comment above), and constructor-
	// field tracking is keyed off a binding's *declaring* function, which
	// for a parameter-standing-in-for-a-binding like this is meaningless
	// -- there is no separate function that "returns" this parameter's
	// own value out of itself. recordReturnedField treats a nil fnObj as
	// a no-op for exactly this reason.
	b.inParamPrepass = true
	b.observeFunctionBody(fd.Body, contexts, paramCancels, paramGroups, &scratch, nil)
	b.inParamPrepass = false
	for _, c := range paramCancels {
		next := c.binding.Called || c.binding.Escapes
		if b.recordParamConsumption(c.cancelObj, next) {
			reportChanged = true
		}
	}
	for _, g := range paramGroups {
		next := g.group.Joined || g.group.Escapes
		if b.recordParamConsumption(g.obj, next) {
			reportChanged = true
		}
	}
	return reportChanged
}

// recordParamConsumption merges next into b.paramConsumption[obj] via OR
// and reports whether the recorded state changed: either the value rose
// from false to true, or this is the first time obj has been recorded at
// all (a lookup miss and a recorded "false" are different things --
// argumentConsumed's pending result depends on telling them apart -- so
// recording "false" for the first time is still a real change dependents
// need to see).
func (b *builder) recordParamConsumption(obj types.Object, next bool) bool {
	prev, existed := b.paramConsumption[obj]
	merged := prev || next
	b.paramConsumption[obj] = merged
	return !existed || merged != prev
}

// argumentConsumed reports whether obj, passed directly as call's argument
// at some position -- a direct argument, or one element of a `...`-spread
// composite literal (see argumentIndexOf) -- is consumed by the callee's
// own body: for a same-package callee, a pure lookup into
// b.paramConsumption, populated by computeParameterConsumption's
// fixed-point loop; for a callee outside the current package, a
// versioned fact via Input.LookupParamConsumption, the same way
// Input.LookupFunctionSummary already answers the equivalent question for
// goroutine targets. This never triggers a fresh analysis of the
// callee's body (unlike, say, functionSummary's on-demand fallback),
// which is what keeps it safe from recursion regardless of how functions
// call each other: a lookup miss just means verified is false, not that
// anything gets computed here.
//
// verified is false for two quite different reasons, which pending tells
// apart:
//
//   - pending == false: the check can never be made with confidence for
//     this call, no matter how many times it's retried -- the callee
//     can't be resolved at all (see resolveCalleeFunc: not a statically
//     named function, nor a local variable/parameter provably assigned
//     to one exactly once), or obj isn't found at a resolvable argument
//     position (see argumentIndexOf: not a direct argument expression,
//     and not an element of a directly- or once-assigned composite
//     literal spread into a matching variadic parameter). Callers should
//     fall back to the prior unconditional "assume the obligation was
//     transferred" behavior here, never assume a leak from an inability
//     to check.
//   - pending == true: the callee is a real, resolvable same-package
//     function and its parameter was identified, but computeParameterConsumption
//     hasn't recorded a result for that parameter yet -- either this
//     iteration of Build's fixed-point loop hasn't reached it yet, or (if
//     the fixed point has already finished, i.e. outside computeParameterConsumption's
//     own call into observeFunctionBody) the pre-pass never reached the
//     callee at all, e.g. max_functions truncation. Inside the fixed
//     point, this should be treated as "no answer yet" and retried on a
//     later sweep, not as "assume transferred", or the fixed point would
//     just reproduce the old single-pass, declaration-order-dependent
//     result. Once the fixed point has finished, a lingering pending case
//     can only be the truncation scenario, and should fall back the same
//     as the never-resolvable case. A cross-package callee is never
//     pending: a fact is either present (verified) or absent (falls
//     back) on the spot -- there is no sweep to wait for, since this
//     build has no way to ever compute that package's own result itself.
func (b *builder) argumentConsumed(call *ast.CallExpr, obj types.Object) (consumed, verified, pending bool) {
	funcObj := b.resolveCalleeFunc(call.Fun)
	if funcObj == nil {
		return false, false, false
	}
	index := argumentIndexOf(call, obj, funcObj, b.in.Info, b.singleAssignTargets)
	if index == -1 {
		return false, false, false
	}
	if decl := b.funcs[funcObj]; decl != nil {
		paramObj := paramObjectAtIndex(b.in.Info, decl, index)
		if paramObj == nil {
			return false, false, false
		}
		paramConsumed, known := b.paramConsumption[paramObj]
		return paramConsumed, known, !known
	}
	if b.in.LookupParamConsumption != nil {
		if paramConsumed, known := b.in.LookupParamConsumption(funcObj, index); known {
			return paramConsumed, true, false
		}
	}
	return false, false, false
}

// resolveCalleeFunc resolves call's callee to a *types.Func for
// argumentConsumed's purposes: first via the ordinary calledObject
// resolution (a directly, statically named function or method), then,
// only if that fails, via singleAssignTargets' narrow single-assignment
// local/parameter resolution -- `var f func(context.CancelFunc) = drop`
// (or the equivalent `:=` or plain `=` form), never reassigned anywhere
// else in the function, called later as `f(cancel)`. This is
// deliberately scoped to this one check: it does not change how a direct
// call to the tracked cancel/group value itself is recognized
// (observeCall's own funObj match still only matches a bare identifier
// via calledObject), nor how a goroutine's target function is resolved
// (buildGoroutine keeps its own, separately-reasoned, more conservative
// behavior for a local variable of function type; see firstStartTarget's
// doc comment for why that one stays as is).
func (b *builder) resolveCalleeFunc(fun ast.Expr) *types.Func {
	if fn, ok := calledObject(fun, b.in.Info).(*types.Func); ok {
		return fn
	}
	id, ok := fun.(*ast.Ident)
	if !ok {
		return nil
	}
	obj := b.in.Info.ObjectOf(id)
	if obj == nil {
		return nil
	}
	target, ok := b.singleAssignTargets[obj].(*ast.Ident)
	if !ok {
		return nil
	}
	fn, _ := b.in.Info.ObjectOf(target).(*types.Func)
	return fn
}

// argumentIndexOf returns the parameter index obj is passed at: either a
// direct argument (a bare identifier expression, matching the previous,
// unconditional behavior exactly), or, when call uses `...`, the
// callee's variadic parameter index (necessarily its last parameter,
// since that is the only position `...` can ever target) when the
// spread expression -- directly, or through one single-assignment level
// of indirection (see compositeLitOf) -- is a composite literal listing
// obj as one of its elements and the callee's variadic element type is
// itself cancel-like (computeParameterConsumption's variadicCancelParam
// is what will have computed *that* parameter's own consumed value, via
// variadicCancelElementCalled, not the ordinary scalar Called/Escapes
// check). Any other spread source (a slice built by append, returned
// from a call, or reassigned more than once) is not attempted -- that
// would mean tracing how the slice was built or flowed, a fundamentally
// larger analysis than this narrow, purely syntactic check -- and
// returns -1, same as any other unresolvable shape.
func argumentIndexOf(call *ast.CallExpr, obj types.Object, funcObj *types.Func, info *types.Info, singleAssign map[types.Object]ast.Expr) int {
	if call.Ellipsis.IsValid() {
		if len(call.Args) == 0 {
			return -1
		}
		lit := spreadCompositeLitOf(call.Args[len(call.Args)-1], info, singleAssign)
		if lit == nil || !compositeLitContainsObject(lit, obj, info) {
			return -1
		}
		sig, ok := funcObj.Type().Underlying().(*types.Signature)
		if !ok || !sig.Variadic() {
			return -1
		}
		return sig.Params().Len() - 1
	}
	for i, arg := range call.Args {
		if identObject(arg, info) == obj {
			return i
		}
	}
	return -1
}

// collectSpreadClaims marks every entry in cancels whose object appears
// (directly, or via one single-assignment level of indirection -- see
// spreadCompositeLitOf) inside a composite literal spread (`...`) into
// some call's last argument, as claimed: observeContainerEscape's
// generic "stored in any composite literal => transferred" fallback must
// not decide this binding's fate, the same way it already defers to
// collectFieldCaptures for the struct-field-capture shape. The real
// verdict is decided precisely, per call, by observeCall's own
// argumentConsumed check when that call node is visited during the main
// walk -- this claim only stops the generic fallback from pre-empting
// that more precise answer with an unconditional "yes, transferred". It
// does not matter here whether the callee turns out resolvable: an
// unresolvable callee still correctly falls back to "assume transferred"
// inside observeCall itself (argumentConsumed reports verified=false
// there too), just via the intended path instead of this blunter one
// firing first.
// collectContainerCaptures finds every local variable in body that is
// assigned exactly once (singleAssignmentTargets) to a slice or map
// composite literal -- keyed or unkeyed, key-vs-position makes no
// difference to a container's own elements -- containing one or more of
// cancels' own tracked objects as an element (slice) or value (map), and
// verifies each such binding directly against whether that same variable
// is later ranged over and called anywhere in body via the single
// recognized idiom `for _, v := range container { v() }`
// (variadicCancelElementCalled, which already only depends on the
// object being ranged over, not on it being a function parameter
// specifically, so it is reused here unchanged). This is the slice/map
// half of "storing a cancel/group value into an arbitrary container is
// still treated as an unverified ownership transfer" (docs/limitations.md)
// -- narrowly extended, not removed: a container built by append,
// returned from a call, or reassigned more than once is left unresolved
// by singleAssignmentTargets the same as it is everywhere else this file
// uses it; a container passed on to a further function instead of ranged
// over directly in the same function falls back to the ordinary
// unverified default; and group values are deliberately not covered at
// all here -- Wait()/Add()/Done() is a multi-call protocol, not the
// single per-element call this idiom checks for, and a slice of group
// values has no equivalent single recognized consuming shape.
func (b *builder) collectContainerCaptures(body *ast.BlockStmt, cancels []*cancelState, info *types.Info, singleAssign map[types.Object]ast.Expr, claimed map[types.Object]bool) {
	if len(cancels) == 0 {
		return
	}
	for obj, rhs := range singleAssign {
		lit, ok := rhs.(*ast.CompositeLit)
		if !ok {
			continue
		}
		var contained []*cancelState
		for _, c := range cancels {
			if c.cancelObj != nil && !claimed[c.cancelObj] && compositeLitContainsObject(lit, c.cancelObj, info) {
				contained = append(contained, c)
			}
		}
		if len(contained) == 0 {
			continue
		}
		consumed := variadicCancelElementCalled(body, obj, info)
		// A container not (yet) shown to be range-consumed is only a
		// confirmed drop if it also has no other escaping use anywhere in
		// body -- passed to a further function, returned, or stored into
		// another container. Any of those means this container's fate
		// isn't decided within this one function, so it must not be
		// claimed at all: the existing unverified "assume transferred"
		// default is the safe, correct answer there, the same one-hop
        // boundary every other direct-verification path in this file
        // already stops at, rather than a false "definitely leaked".
		if !consumed && containerHasOtherEscapingUse(body, obj, info) {
			continue
		}
		for _, c := range contained {
			claimed[c.cancelObj] = true
			if consumed {
				c.binding.Escapes = true
				c.binding.Evidence = append(c.binding.Evidence, model.Evidence{Kind: "parameter-consumed", Message: "stored in a slice/map literal that is later ranged over and called", Span: ptrSpan(b.span(lit))})
			} else {
				c.binding.Evidence = append(c.binding.Evidence, model.Evidence{Kind: "parameter-not-consumed", Message: "stored in a slice/map literal, but that variable is never ranged over and called", Span: ptrSpan(b.span(lit))})
			}
		}
	}
}

// containerHasOtherEscapingUse reports whether obj (a container variable
// already known to be assigned exactly once, to a composite literal) is
// used anywhere in body in a way collectContainerCaptures cannot itself
// resolve: passed as an argument to some call, returned, or stored as an
// element of another composite literal. A range statement's own `range
// obj` slot is a different node kind (*ast.RangeStmt, not matched by any
// case below) and so never counts as an "other" use here -- that is the
// one shape collectContainerCaptures already resolves directly via
// variadicCancelElementCalled, not something this needs to also flag.
func containerHasOtherEscapingUse(body ast.Node, obj types.Object, info *types.Info) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch x := n.(type) {
		case *ast.CallExpr:
			for _, arg := range x.Args {
				if identObject(arg, info) == obj {
					found = true
					return false
				}
			}
		case *ast.ReturnStmt:
			for _, r := range x.Results {
				if identObject(r, info) == obj {
					found = true
					return false
				}
			}
		case *ast.CompositeLit:
			if compositeLitContainsObject(x, obj, info) {
				found = true
				return false
			}
		case *ast.AssignStmt:
			for i, rhs := range x.Rhs {
				if identObject(rhs, info) != obj {
					continue
				}
				// `_ = obj` is the standard explicit-discard idiom, not a
				// real further use -- excluding it is what lets the
				// genuinely-dropped case (a container built and then
				// discarded, with no range consumption anywhere) still be
				// reported, rather than this check itself suppressing it.
				if i < len(x.Lhs) {
					if id, ok := x.Lhs[i].(*ast.Ident); ok && id.Name == "_" {
						continue
					}
				}
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func collectSpreadClaims(body *ast.BlockStmt, cancels []*cancelState, info *types.Info, singleAssign map[types.Object]ast.Expr, claimed map[types.Object]bool) {
	if len(cancels) == 0 {
		return
	}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !call.Ellipsis.IsValid() || len(call.Args) == 0 {
			return true
		}
		lit := spreadCompositeLitOf(call.Args[len(call.Args)-1], info, singleAssign)
		if lit == nil {
			return true
		}
		for _, c := range cancels {
			if c.cancelObj != nil && compositeLitContainsObject(lit, c.cancelObj, info) {
				claimed[c.cancelObj] = true
			}
		}
		return true
	})
}

// spreadCompositeLitOf resolves expr to a composite literal: directly, or
// (when expr is a bare identifier) through exactly one local,
// single-assignment level of indirection via singleAssign --
// `args := []T{cancel}; f(args...)` is exactly as staticaly readable as
// `f([]T{cancel}...)`, just one control-flow-free step further back.
// Distinct from the unrelated, unwrap-through-`&`/parens compositeLitOf
// above (that one is for the constructor/field-ownership feature's
// different question -- "what literal does this expression construct" --
// and does not resolve through a variable at all).
func spreadCompositeLitOf(expr ast.Expr, info *types.Info, singleAssign map[types.Object]ast.Expr) *ast.CompositeLit {
	if lit, ok := expr.(*ast.CompositeLit); ok {
		return lit
	}
	id, ok := expr.(*ast.Ident)
	if !ok {
		return nil
	}
	obj := info.ObjectOf(id)
	if obj == nil {
		return nil
	}
	lit, _ := singleAssign[obj].(*ast.CompositeLit)
	return lit
}

// compositeLitContainsObject reports whether obj appears as one of lit's
// elements, unwrapping a keyed element's value (`[]T{0: cancel}`) the
// same as a plain one.
func compositeLitContainsObject(lit *ast.CompositeLit, obj types.Object, info *types.Info) bool {
	for _, elt := range lit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			elt = kv.Value
		}
		if identObject(elt, info) == obj {
			return true
		}
	}
	return false
}

// singleAssignmentTargets finds every local variable or parameter in
// body that is assigned exactly once, anywhere in body (including inside
// nested function literals -- an assignment there still counts, since
// it's the same underlying object and this needs to rule out every
// possible write, not just top-level ones), and records that one
// assignment's right-hand side expression, whatever shape it turns out
// to be. This is a narrow, purely syntactic (no control-flow, no
// points-to) resolution: "exactly once" is what makes it sound without
// needing to trace which assignment reaches which use -- if a variable
// is written in only one place in the entire function, whatever value it
// holds at any later use must be either that write's value or the
// type's zero value (a nil func or nil slice, meaning any use through it
// would already panic, or range over nothing, before this analysis is
// ever consulted) -- there is no other value it could hold. Two call
// sites currently consult this, each interpreting the recorded
// expression narrowly for its own purpose (see resolveCalleeFunc and
// compositeLitOf); neither attempts anything like general alias analysis
// (docs/roadmap.md item 5, an optional full SSA-backed alias/call-graph
// experiment, not this).
func singleAssignmentTargets(body *ast.BlockStmt, info *types.Info) map[types.Object]ast.Expr {
	counts := map[types.Object]int{}
	targets := map[types.Object]ast.Expr{}
	record := func(lhs, rhs ast.Expr) {
		id, ok := lhs.(*ast.Ident)
		if !ok || id.Name == "_" {
			return
		}
		obj := info.ObjectOf(id)
		if obj == nil {
			return
		}
		counts[obj]++
		if counts[obj] == 1 && rhs != nil {
			targets[obj] = rhs
		} else {
			delete(targets, obj)
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range x.Lhs {
				if i < len(x.Rhs) {
					record(lhs, x.Rhs[i])
				} else {
					record(lhs, nil)
				}
			}
		case *ast.ValueSpec:
			for i, name := range x.Names {
				if i < len(x.Values) {
					record(name, x.Values[i])
				} else {
					record(name, nil)
				}
			}
		}
		return true
	})
	return targets
}

// variadicCancelElementCalled reports whether body demonstrably calls
// every element of containerObj -- a variadic parameter or plain local
// slice/map variable, either way whose element type is cancel-like -- via
// the single recognized idiom `for _, v := range container { v() }` (any
// range-loop variable name; called with no arguments; matched by the
// loop variable's own object, not its name). Despite the name (kept for
// its original, narrower call site -- computeParameterConsumption's
// variadic-parameter case), this only depends on containerObj being
// ranged over, not on it being a parameter specifically, so
// collectContainerCaptures also reuses it unchanged for a plain local
// slice/map variable. This is deliberately narrow, the collection
// equivalent of the direct-call check already used for a scalar cancel
// binding: it does not follow the loop variable any further (storing it,
// passing it on, or calling it only conditionally within the loop body
// are all left unrecognized, falling back exactly like an unresolvable
// case would), and it does not attempt index-based access
// (`container[i]()`) or a range target reached through any indirection.
func variadicCancelElementCalled(body *ast.BlockStmt, containerObj types.Object, info *types.Info) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		rng, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		if identObject(rng.X, info) != containerObj {
			return true
		}
		loopVar, ok := rng.Value.(*ast.Ident)
		if !ok || loopVar.Name == "_" {
			return true
		}
		loopObj := info.Defs[loopVar]
		if loopObj == nil {
			return true
		}
		ast.Inspect(rng.Body, func(m ast.Node) bool {
			if found {
				return false
			}
			call, ok := m.(*ast.CallExpr)
			if ok && len(call.Args) == 0 && identObject(call.Fun, info) == loopObj {
				found = true
				return false
			}
			return true
		})
		return false
	})
	return found
}

// exportedParamConsumption returns, for each of fd's own cancel-like,
// group-like, or variadic-cancel-collection parameters, by position,
// whether computeParameterConsumption's fixed point (already fully
// converged by the time buildFunction runs -- Build's pre-pass sweep
// loop always finishes before its main per-function loop begins)
// recorded it as consumed. This becomes model.Function.ParamConsumption,
// which a cross-package caller can consult the same way argumentConsumed
// already consults b.paramConsumption directly for a same-package one --
// see analyzer.go's fact export and Input.LookupParamConsumption.
func (b *builder) exportedParamConsumption(fd *ast.FuncDecl) map[int]bool {
	if fd.Type.Params == nil {
		return nil
	}
	var out map[int]bool
	record := func(index int, obj types.Object) {
		consumed, ok := b.paramConsumption[obj]
		if !ok {
			return
		}
		if out == nil {
			out = map[int]bool{}
		}
		out[index] = consumed
	}
	index := 0
	params := fd.Type.Params.List
	for fieldIdx, field := range params {
		for nameIdx, name := range field.Names {
			obj := b.in.Info.ObjectOf(name)
			if obj != nil && name.Name != "_" {
				if isCancelFuncType(obj.Type()) || groupKind(obj.Type()) != "" {
					record(index, obj)
				} else if fieldIdx == len(params)-1 && nameIdx == len(field.Names)-1 {
					if _, ok := field.Type.(*ast.Ellipsis); ok {
						if slice, ok := obj.Type().(*types.Slice); ok && isCancelFuncType(slice.Elem()) {
							record(index, obj)
						}
					}
				}
			}
			index++
		}
		if len(field.Names) == 0 {
			index++
		}
	}
	return out
}

// exportedParamDoneCalled returns, for each of fd's own
// sync.WaitGroup-typed parameters, by position, whether
// computeParamDoneCalled's fixed point (already fully converged by the
// time buildFunction runs) recorded Done() as eventually called on it.
// This becomes model.Function.ParamDoneCalled -- see analyzer.go's fact
// export and Input.LookupParamDoneCalled.
func (b *builder) exportedParamDoneCalled(fd *ast.FuncDecl) map[int]bool {
	if fd.Type.Params == nil {
		return nil
	}
	var out map[int]bool
	index := 0
	for _, field := range fd.Type.Params.List {
		for _, name := range field.Names {
			obj := b.in.Info.ObjectOf(name)
			if obj != nil && name.Name != "_" && groupKind(obj.Type()) == "waitgroup" && b.paramDoneCalled[obj] {
				if out == nil {
					out = map[int]bool{}
				}
				out[index] = true
			}
			index++
		}
		if len(field.Names) == 0 {
			index++
		}
	}
	return out
}

// paramObjectAtIndex returns the *types.Var for fd's parameter at the
// given zero-based position, flattening multi-name parameter groups
// (func f(a, b context.CancelFunc) has a at 0, b at 1). Returns nil for an
// unnamed parameter (nothing to resolve) or an out-of-range index.
func paramObjectAtIndex(info *types.Info, fd *ast.FuncDecl, index int) types.Object {
	if fd.Type.Params == nil {
		return nil
	}
	i := 0
	for _, field := range fd.Type.Params.List {
		if len(field.Names) == 0 {
			if i == index {
				return nil
			}
			i++
			continue
		}
		for _, name := range field.Names {
			if i == index {
				return info.ObjectOf(name)
			}
			i++
		}
	}
	return nil
}

// spreadArgumentContains reports whether obj appears inside the
// composite literal that call's spread argument resolves to via exactly
// one single-assignment level of indirection (spreadCompositeLitOf) --
// the one case objectsUsedInExpressions' plain identifier walk cannot
// already see on its own, since it has no reason to follow a variable to
// its own, single, prior assignment. A composite literal spelled out
// directly at the call site is already found by that ordinary walk
// (ast.Inspect descends straight into it) and does not need this.
func spreadArgumentContains(call *ast.CallExpr, obj types.Object, info *types.Info, singleAssign map[types.Object]ast.Expr) bool {
	if !call.Ellipsis.IsValid() || len(call.Args) == 0 {
		return false
	}
	id, ok := call.Args[len(call.Args)-1].(*ast.Ident)
	if !ok {
		return false
	}
	spreadObj := info.ObjectOf(id)
	if spreadObj == nil {
		return false
	}
	lit, ok := singleAssign[spreadObj].(*ast.CompositeLit)
	if !ok {
		return false
	}
	return compositeLitContainsObject(lit, obj, info)
}

func (b *builder) observeCall(call *ast.CallExpr, cancels []*cancelState, groups []*groupState) {
	name := b.callName(call)
	funObj := calledObject(call.Fun, b.in.Info)
	argObjects := objectsUsedInExpressions(call.Args, b.in.Info, true)
	for _, c := range cancels {
		if c.cancelObj != nil && funObj == c.cancelObj {
			c.binding.Called = true
			c.callSites = append(c.callSites, call)
			c.binding.Evidence = append(c.binding.Evidence, model.Evidence{Kind: "cancel-call", Message: "cancellation function is called", Span: ptrSpan(b.span(call))})
		}
		// Passing a cancel function as an argument is treated as a
		// conservative ownership transfer, UNLESS Phase 5's direct-
		// parameter-passing check (docs/cfg-migration-plan.md) can verify
		// what actually happens: when the callee is a resolvable
		// same-package function and its own body demonstrably never
		// consumes the corresponding parameter, that is a real, checked
		// leak, not an unverified pass-through -- Escapes stays false and
		// LL1001 fires. Whenever the check can't be made with confidence,
		// argumentConsumed reports verified=false and this falls back to
		// the prior unconditional behavior -- except mid-fixed-point,
		// where a pending (not yet computed, but real and resolvable)
		// dependency must be left alone rather than assumed transferred,
		// or the interprocedural fixed point (computeParameterConsumption)
		// would just freeze at whatever a single declaration-order pass
		// happened to see, reintroducing the order dependence it exists to
		// remove. Whether the call's result is discarded is unrelated to
		// the callee receiving the function, so no assignment-context
		// guard applies either way.
		if c.cancelObj != nil && (hasObject(argObjects, c.cancelObj) || spreadArgumentContains(call, c.cancelObj, b.in.Info, b.singleAssignTargets)) {
			if consumed, verified, pending := b.argumentConsumed(call, c.cancelObj); verified {
				if consumed {
					c.binding.Escapes = true
					c.binding.Evidence = append(c.binding.Evidence, model.Evidence{Kind: "parameter-consumed", Message: "passed as an argument; the callee's own body consumes it", Span: ptrSpan(b.span(call))})
				} else {
					c.binding.Evidence = append(c.binding.Evidence, model.Evidence{Kind: "parameter-not-consumed", Message: "passed as an argument, but the callee's own body never calls or further transfers it", Span: ptrSpan(b.span(call))})
				}
			} else if pending && b.inParamPrepass {
				// No answer yet this sweep; leave the binding as-is. If
				// the callee really does consume it, that will surface as
				// consumed==true here on a later sweep and this
				// function's own recorded result will rise to match.
			} else {
				c.binding.Escapes = true
			}
		}
	}
	for _, g := range groups {
		receiver := selectorReceiverObject(call.Fun, b.in.Info)
		if receiver == g.obj {
			switch selectorMethod(call.Fun) {
			case "Add", "Go":
				g.group.Starts++
				g.group.Evidence = append(g.group.Evidence, model.Evidence{Kind: "worker-start", Message: "worker accounting starts here", Span: ptrSpan(b.span(call))})
			case "Wait":
				g.group.Joined = true
				g.waitCallSites = append(g.waitCallSites, call)
				g.group.Evidence = append(g.group.Evidence, model.Evidence{Kind: "join", Message: "group is joined here", Span: ptrSpan(b.span(call))})
			}
		}
		usesGroup := hasObject(argObjects, g.obj)
		if b.hasWrapper(b.joinWrappers, name) && usesGroup {
			g.group.Joined = true
		}
		if receiver != g.obj && usesGroup && !b.hasWrapper(b.joinWrappers, name) {
			// Same Phase 5 verification as cancels above, applied to a
			// group passed as an argument rather than joined directly --
			// but with one deliberate divergence from the cancel case: a
			// cancel binding has only one further-life question worth
			// asking ("does something eventually call it"), so Escapes is
			// the right signal there regardless of *how* consumption was
			// verified. A group has several more questions Escapes was
			// silently answering wrong for: count-interval accounting
			// (CountMismatch, computeGroupBalances) and the ordering
			// checks (computeGroupOrdering) are each independently
			// verified from the group's own recorded Add/Done/Wait call
			// sites and do not depend on *who* calls Wait -- but engine.go
			// skips a group's diagnostics entirely whenever Escapes is
			// set, before any of those other checks are even consulted.
			// Recording a verified helper-mediated join as Escapes (as if
			// it were an unverified pass-elsewhere, the same as the
			// cancel case) silently discarded a real, already-computed
			// CountMismatch finding along with it -- a group passed to a
			// helper that calls Wait() on it, but started with an Add(n)
			// no spawned Done() actually matches, was reported clean.
			// Recording it as Joined instead keeps that finding live: an
			// outstanding literal imbalance is exactly as real regardless
			// of whether Wait() is called directly or through a helper.
			// (JoinedOnAllPaths and StopAfterWait stay unestablished for a
			// helper-mediated join either way, the same as for a
			// join_wrapper call: neither has a direct Wait() call site in
			// *this* function's own CFG to anchor to. That is an existing,
			// separately documented limitation, not something this
			// change affects.)
			if consumed, verified, pending := b.argumentConsumed(call, g.obj); verified {
				if consumed {
					g.group.Joined = true
					g.group.Evidence = append(g.group.Evidence, model.Evidence{Kind: "parameter-consumed", Message: "passed as an argument; the callee's own body joins or further transfers it", Span: ptrSpan(b.span(call))})
				} else {
					g.group.Evidence = append(g.group.Evidence, model.Evidence{Kind: "parameter-not-consumed", Message: "passed as an argument, but the callee's own body never joins or further transfers it", Span: ptrSpan(b.span(call))})
				}
			} else if pending && b.inParamPrepass {
				// No answer yet this sweep; see the cancel case above.
			} else {
				g.group.Escapes = true
			}
		}
	}
}

func (b *builder) observeReturn(ret *ast.ReturnStmt, cancels []*cancelState, groups []*groupState, claimed map[types.Object]bool) {
	used := objectsUsedInExpressions(ret.Results, b.in.Info, true)
	for _, c := range cancels {
		if c.cancelObj != nil && hasObject(used, c.cancelObj) && !claimed[c.cancelObj] {
			c.binding.Escapes = true
		}
	}
	for _, g := range groups {
		if g.obj != nil && hasObject(used, g.obj) && !claimed[g.obj] {
			g.group.Escapes = true
		}
	}
}

func (b *builder) observeEscapeAssignment(as *ast.AssignStmt, cancels []*cancelState, groups []*groupState) {
	for i, rhs := range as.Rhs {
		obj := identObject(rhs, b.in.Info)
		if obj == nil || pairedLHSIsBlank(as, i) {
			continue
		}
		lhsObj := aliasTargetObject(as, i, b.in.Info)
		for _, c := range cancels {
			if obj != c.cancelObj {
				continue
			}
			if alias := cancelBindingFor(cancels, lhsObj); alias != nil {
				if alias != c {
					c.pendingAliasEscapes = append(c.pendingAliasEscapes, alias)
				}
				continue
			}
			c.binding.Escapes = true
		}
		for _, g := range groups {
			if obj != g.obj {
				continue
			}
			if alias := groupBindingFor(groups, lhsObj); alias != nil {
				if alias != g {
					g.pendingAliasEscapes = append(g.pendingAliasEscapes, alias)
				}
				continue
			}
			g.group.Escapes = true
		}
	}
}

// aliasTargetObject returns the types.Object that the i-th name on as's
// left-hand side defines (`:=`) or assigns to (`=`), if that name is a
// plain identifier other than the blank identifier -- nil for any lvalue
// this function doesn't resolve to a specific object (a selector like
// `h.wg`, an index expression, or an out-of-range index, none of which
// pairedLHSIsBlank's blank-identifier check already screens out).
func aliasTargetObject(as *ast.AssignStmt, i int, info *types.Info) types.Object {
	if i >= len(as.Lhs) {
		return nil
	}
	id, ok := as.Lhs[i].(*ast.Ident)
	if !ok || id.Name == "_" {
		return nil
	}
	if as.Tok == token.DEFINE {
		if obj := info.Defs[id]; obj != nil {
			return obj
		}
	}
	return info.ObjectOf(id)
}

func cancelBindingFor(cancels []*cancelState, obj types.Object) *cancelState {
	if obj == nil {
		return nil
	}
	for _, c := range cancels {
		if c.cancelObj == obj {
			return c
		}
	}
	return nil
}

func groupBindingFor(groups []*groupState, obj types.Object) *groupState {
	if obj == nil {
		return nil
	}
	for _, g := range groups {
		if g.obj == obj {
			return g
		}
	}
	return nil
}

// resolveAliasEscapes finalizes every pending alias-escape relationship
// observeEscapeAssignment recorded while walking a function body: taking
// a tracked cancel/group's value (its address, for a group; the function
// value itself, for a cancel) and assigning it into ANOTHER local
// variable that is itself a separately tracked cancel/group binding is
// not, by itself, evidence that the original value was transferred
// anywhere -- only evidence that a local alias for it now exists. Whether
// that alias was ever put to any use is exactly what the alias's own
// binding, independently, has already recorded by the time the whole
// body has been walked (observeFunctionBody runs the identical generic
// call/escape observation against the alias's own object, the same as it
// does for any other locally-declared binding): if the alias itself
// shows no activity at all -- never called or started via it, never
// itself joined, never itself further escaped -- then aliasing the
// original into it is exactly as inert as never mentioning the original
// value's address/function value at all, and should not discharge the
// original's own "was this ever called/joined" obligation. Confirmed
// concretely: `var a, b sync.WaitGroup; p := &a; p = &b; b.Add(1)` with
// no Wait() anywhere and p never otherwise used previously suppressed
// LL1003 for "b" entirely (b.group.Escapes was set the moment `p = &b`
// assigned its address into ANY tracked local, regardless of whether
// that local was ever subsequently used for anything) -- exactly the
// same as the far simpler, single-assignment `wg.Add(1); go
// worker(&wg)-shaped goroutine; p := &wg` with no reassignment and no
// further use of p at all.
//
// If the alias DOES show activity, this still cannot verify that the
// activity reflects what happened to *this specific* original value: a
// later reassignment (`p := &a; p = &b`) can make an alias's own
// eventual activity belong to a different original value than the one
// under consideration, and this function does not attempt to disentangle
// that (a materially larger undertaking -- see docs/limitations.md's
// "aliasing is shallow" boundary). So, conservatively, matching this
// project's "unverified defaults to safe" policy throughout, the original
// is marked Escapes in that case, exactly as it always was before this
// resolution step existed.
//
// This runs to a fixed point rather than a single pass so that a chain
// of aliases (an alias whose own only activity is itself being an inert
// or active alias of something further) resolves correctly regardless of
// the order bindings happen to appear in cancels/groups; each pass can
// only ever set Escapes (never clear it), so it is guaranteed to
// terminate within at most len(cancels)+len(groups) passes.
//
// A second, unconditional rule runs alongside the one above, but for
// *groups only*, never cancels -- an asymmetry that reflects a real
// difference in what a tracked binding means for each. collectBindings
// tracks every *sync.WaitGroup/*errgroup.Group-typed local exactly like
// a directly-declared value -- necessarily so, since that is also the
// shape of an ordinary function *parameter* receiving a group by
// pointer, the common case this recognition exists for -- but a local
// variable of that pointer type that is also, at some point, assigned
// the address of an *already independently tracked* group in the same
// function is something else: a plain alias for the exact same
// underlying value, not a second, independent group. A cancel binding
// has no equivalent phantom-identity risk: collectBindings only ever
// creates one by recognizing the specific `x, cancel := factory(...)`
// constructor-call shape, so every cancelState in cancels represents a
// genuinely separate construction event, never a bare alias for a
// pointer type the way a group can be. Consequently, `cancel1 = cancel2`
// does not "alias cancel1 into cancel2's already-tracked identity" the
// way `p = &wg` aliases p into wg's -- it overwrites cancel1's own
// variable, permanently losing whatever cancel function it held before
// (there is no address-of/pointer involved, so nothing else can reach
// that original value once this assignment runs), which is exactly the
// scenario LL1001 exists to catch: cancel1's own "was the function it
// once held ever called" question stays fully legitimate and answerable
// as "no, and it now never can be" regardless of what cancel1's name
// goes on to hold afterward. Applying this second rule to cancels too
// would have wrongly suppressed exactly that finding -- confirmed by a
// regression while developing this fix: marking cancel1 Escapes merely
// because cancel2's value was assigned into it silently dropped
// cancel1's own genuine lost-cancel finding for a value that no longer
// has any surviving reference at all. See
// TestCancelIdentity_ReassignedAliasDoesNotSuppressEitherCancel.
//
// Confirmed concretely for the group case this rule does apply to:
// `var wg sync.WaitGroup; var p *sync.WaitGroup; p = &wg; p.Add(1); go
// func(){ wg.Done() }(); wg.Wait()` is entirely correct, safe code (wg's
// counter genuinely goes 0->1->0 and Wait() genuinely unblocks), but
// before this rule existed it produced a confirmed false positive:
// LL1003 fired on "p" specifically, because p.Add(1) is attributed to
// p's own independent binding (Starts=1) while wg.Done() and wg.Wait()
// are attributed to wg's own, entirely separate binding (which stays
// silent regardless, since wg.Starts==0 excludes it) -- leaving p
// accounting for a start with no join ever observed through its own
// name specifically. Since this project treats a false positive as
// categorically worse than a false negative throughout, this rule is
// unconditional rather than activity-gated the way the first rule above
// is: even a real, active use of the alias is not safe to diagnose on
// its own once it is known that some *other*, already-tracked group was
// assigned into it at some point, since the alias's own accounting may
// reflect calls meant for a different underlying value than whichever
// one is actually live at any given point (this analysis does not track
// that, by design -- see docs/limitations.md's "aliasing is shallow"
// boundary). This intentionally costs coverage on the alias's own name
// specifically -- it can no longer independently surface a real bug that
// shows up *only* through calls made via the alias and never through the
// original's own name -- in exchange for never again reporting one that
// only exists because of this identity split. A real diagnostic on the
// *original* binding's own name remains fully available and unaffected
// by this rule, exactly as it always has been.
func resolveAliasEscapes(cancels []*cancelState, groups []*groupState) {
	for changed := true; changed; {
		changed = false
		for _, c := range cancels {
			if c.binding.Escapes {
				continue
			}
			for _, alias := range c.pendingAliasEscapes {
				if alias.binding.Called || alias.binding.Escapes {
					c.binding.Escapes = true
					changed = true
					break
				}
			}
		}
		for _, g := range groups {
			if g.group.Escapes {
				continue
			}
			for _, alias := range g.pendingAliasEscapes {
				if alias.group.Starts > 0 || alias.group.Joined || alias.group.Escapes {
					g.group.Escapes = true
					changed = true
					break
				}
			}
		}
	}
	for _, g := range groups {
		for _, alias := range g.pendingAliasEscapes {
			alias.group.Escapes = true
		}
	}
}

func pairedLHSIsBlank(as *ast.AssignStmt, rhsIndex int) bool {
	if len(as.Rhs) != len(as.Lhs) || rhsIndex >= len(as.Lhs) {
		return false
	}
	id, ok := as.Lhs[rhsIndex].(*ast.Ident)
	return ok && id.Name == "_"
}

func (b *builder) observeContainerEscape(n ast.Node, cancels []*cancelState, groups []*groupState, claimed map[types.Object]bool) {
	// A ValueSpec contains both newly defined identifiers and initializers. Only
	// initializer expressions can store an already-existing lifecycle value.
	nodes := []ast.Node{n}
	if spec, ok := n.(*ast.ValueSpec); ok {
		nodes = nodes[:0]
		for _, value := range spec.Values {
			nodes = append(nodes, value)
		}
	}
	for _, node := range nodes {
		used := objectsUsed(node, b.in.Info, true)
		for _, c := range cancels {
			// claimed holds every binding already accounted for by the
			// narrower, identity-tracked field-capture mechanism
			// (collectFieldCaptures/resolveFieldCaptures) -- skipping it
			// here defers to that mechanism's own, more precise verdict
			// instead of this generic fallback's unconditional "stored
			// anywhere => transferred".
			if c.cancelObj != nil && hasObject(used, c.cancelObj) && !claimed[c.cancelObj] {
				c.binding.Escapes = true
				c.binding.Evidence = append(c.binding.Evidence, model.Evidence{Kind: "ownership-transfer", Message: "cancellation function is stored in another value", Span: ptrSpan(b.span(node))})
			}
		}
		for _, g := range groups {
			if g.obj != nil && hasObject(used, g.obj) && !claimed[g.obj] {
				g.group.Escapes = true
			}
		}
	}
}

// collectFieldCaptures scans body for the two "selected struct fields"
// shapes field/constructor ownership tracking recognizes (docs/roadmap.md
// item 3): a cancel/group binding already recognized in cancels/groups
// used as the value of a named field in a struct composite literal,
// either (a) that literal assigned whole to a single local variable via
// `:=`/`=`/a `var` declaration with an initializer, or (b) that literal
// constructed directly inline as one of a return statement's own result
// expressions. Only a keyed struct literal is recognized -- a positional
// literal, or a literal for a slice/array/map type, is left to the
// existing generic escape fallback untouched, the same as multi-name or
// blank-discarded assignments and any other shape not matching this exact
// narrow pattern. claimed is populated with every binding object captured
// this way, so the caller's generic composite-literal/return escape
// handling knows to defer to resolveFieldCaptures's own verdict instead
// of unconditionally marking it transferred on sight. Nested function
// literals have independent locals and are not descended into, matching
// the rest of this file's scoping.
func (b *builder) collectFieldCaptures(body *ast.BlockStmt, cancels []*cancelState, groups []*groupState) (captures []*fieldCapture, claimed map[types.Object]bool) {
	claimed = map[types.Object]bool{}
	if body == nil || (len(cancels) == 0 && len(groups) == 0) {
		return nil, claimed
	}
	funcDepth := 0
	var nodeIsFuncLit []bool
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			last := len(nodeIsFuncLit) - 1
			if last >= 0 {
				if nodeIsFuncLit[last] {
					funcDepth--
				}
				nodeIsFuncLit = nodeIsFuncLit[:last]
			}
			return true
		}
		_, isFuncLit := n.(*ast.FuncLit)
		nodeIsFuncLit = append(nodeIsFuncLit, isFuncLit)
		if isFuncLit {
			funcDepth++
		}
		if funcDepth > 0 {
			return true
		}
		switch x := n.(type) {
		case *ast.AssignStmt:
			if x.Tok != token.DEFINE && x.Tok != token.ASSIGN {
				return true
			}
			if len(x.Lhs) != 1 || len(x.Rhs) != 1 {
				return true
			}
			lhs, ok := x.Lhs[0].(*ast.Ident)
			if !ok || lhs.Name == "_" {
				return true
			}
			varObj := b.in.Info.ObjectOf(lhs)
			if varObj == nil {
				return true
			}
			if lit := compositeLitOf(x.Rhs[0]); lit != nil {
				captures = append(captures, b.captureFieldsFromLiteral(lit, varObj, -1, cancels, groups, claimed)...)
			}
		case *ast.ValueSpec:
			if len(x.Names) != 1 || len(x.Values) != 1 || x.Names[0].Name == "_" {
				return true
			}
			varObj := b.in.Info.ObjectOf(x.Names[0])
			if varObj == nil {
				return true
			}
			if lit := compositeLitOf(x.Values[0]); lit != nil {
				captures = append(captures, b.captureFieldsFromLiteral(lit, varObj, -1, cancels, groups, claimed)...)
			}
		case *ast.ReturnStmt:
			for i, res := range x.Results {
				if lit := compositeLitOf(res); lit != nil {
					captures = append(captures, b.captureFieldsFromLiteral(lit, nil, i, cancels, groups, claimed)...)
				}
			}
		}
		return true
	})
	return captures, claimed
}

// captureFieldsFromLiteral inspects one struct composite literal's own
// keyed elements for a value that is exactly one of cancels'/groups'
// tracked binding objects (not a nested sub-expression using it, matching
// identObject's own narrow unwrapping of `&x`/parens only), returning one
// fieldCapture per match found. Not a struct literal at all (a slice,
// array, or map composite literal, or a struct literal using positional
// rather than keyed elements) yields no captures, leaving those bindings
// for the existing generic escape fallback to handle exactly as before.
func (b *builder) captureFieldsFromLiteral(lit *ast.CompositeLit, varObj types.Object, returnIndex int, cancels []*cancelState, groups []*groupState, claimed map[types.Object]bool) []*fieldCapture {
	if !isStructCompositeLit(lit, b.in.Info) {
		return nil
	}
	var out []*fieldCapture
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		valueObj := identObject(kv.Value, b.in.Info)
		if valueObj == nil {
			continue
		}
		for _, c := range cancels {
			if c.cancelObj != nil && valueObj == c.cancelObj {
				claimed[valueObj] = true
				out = append(out, &fieldCapture{varObj: varObj, fieldName: key.Name, cancel: c, returnIndex: returnIndex})
			}
		}
		for _, g := range groups {
			if g.obj != nil && valueObj == g.obj {
				claimed[valueObj] = true
				out = append(out, &fieldCapture{varObj: varObj, fieldName: key.Name, group: g, returnIndex: returnIndex})
			}
		}
	}
	return out
}

// compositeLitOf unwraps expr down to the struct/slice/map composite
// literal it constructs, if any: `&T{...}` (the overwhelmingly common
// shape for a constructor returning a pointer, or for a locally-held
// handle) and a parenthesized literal are both recognized; anything else
// (an existing variable, a function call, a type conversion of something
// other than a literal) returns nil, since those are not a fresh literal
// this function's own body can attribute a field of to a specific value.
func compositeLitOf(expr ast.Expr) *ast.CompositeLit {
	switch x := expr.(type) {
	case *ast.CompositeLit:
		return x
	case *ast.UnaryExpr:
		if x.Op == token.AND {
			return compositeLitOf(x.X)
		}
	case *ast.ParenExpr:
		return compositeLitOf(x.X)
	}
	return nil
}

// isStructCompositeLit reports whether lit's own static type (as recorded
// by the type checker, which is always the pointed-to struct type itself
// even when the literal is immediately addressed with `&`, never the
// pointer type) is a struct. A slice, array, or map composite literal --
// the other kinds sharing the same *ast.CompositeLit node type -- reports
// false, which is what keeps this field-capture mechanism from ever
// attempting to track a binding placed in a container it has no field
// identity for.
func isStructCompositeLit(lit *ast.CompositeLit, info *types.Info) bool {
	t := info.TypeOf(lit)
	if t == nil {
		return false
	}
	_, ok := t.Underlying().(*types.Struct)
	return ok
}

// ancestor returns the node n levels up the stack from its top (n=1 is
// the immediate parent, n=2 the grandparent, and so on), or nil if the
// stack is not that deep. See walkFieldCaptureUses for how stack is built
// and maintained during traversal.
func ancestor(stack []ast.Node, n int) ast.Node {
	if len(stack) < n {
		return nil
	}
	return stack[len(stack)-n]
}

// resolveFieldCaptures is field/constructor ownership tracking's own
// verdict step, run once per function body after collectFieldCaptures has
// found every candidate in it (see observeFunctionBody and
// computeFieldOwnership, its two callers). A capture built directly
// inline in a return statement (returnIndex >= 0) has no further local
// evidence to look for -- a fresh literal born inside the return
// statement can't be referenced again -- so it goes straight to
// recordReturnedField. A capture held by a named local variable is
// resolved against the rest of the body via walkFieldCaptureUses: verified
// consumption needs nothing further (Called/Joined is already set);
// verified as returned (and not otherwise used in a way this narrow check
// can't follow) is handed to recordReturnedField the same way; any other,
// less certain use of the variable falls back to the conservative
// assume-transferred default the rest of this file uses whenever a
// value's fate can't be verified; and a variable neither consumed,
// returned, nor used any other way at all is left exactly as constructed
// (Called/Joined/Escapes all false) so the ordinary LL1001/LL1003 checks
// fire on this positive evidence of an unconsumed capability -- the
// "stored struct" leak this mechanism exists to catch.
func (b *builder) resolveFieldCaptures(fnObj *types.Func, body *ast.BlockStmt, captures []*fieldCapture) {
	if len(captures) == 0 {
		return
	}
	byVar := map[types.Object][]*fieldCapture{}
	for _, c := range captures {
		if c.returnIndex >= 0 {
			b.recordReturnedField(fnObj, c, c.returnIndex)
			continue
		}
		if c.varObj != nil {
			byVar[c.varObj] = append(byVar[c.varObj], c)
		}
	}
	if len(byVar) == 0 {
		return
	}
	consumed := map[*fieldCapture]bool{}
	returnedAt := map[types.Object]int{}
	otherUse := map[types.Object]bool{}
	b.walkFieldCaptureUses(body, byVar, consumed, returnedAt, otherUse)

	for varObj, fcs := range byVar {
		idx, wasReturned := returnedAt[varObj]
		hasOther := otherUse[varObj]
		for _, fc := range fcs {
			if consumed[fc] {
				continue
			}
			switch {
			case hasOther:
				markFieldCaptureFallback(fc)
			case wasReturned:
				b.recordReturnedField(fnObj, fc, idx)
			default:
				markFieldCaptureUnconsumed(fc)
			}
		}
	}
}

// walkFieldCaptureUses is the single whole-body scan every field capture
// in byVar (keyed by the local variable holding it) is resolved against,
// run once per body rather than interleaved with collectFieldCaptures'
// own traversal so that a consuming call or return statement is found
// regardless of its lexical position relative to the capture's own
// definition. It maintains an explicit ancestor stack (the same push-
// before-descend/pop-on-nil-visit idiom observeFunctionBody's own
// funcDepth tracking already uses) so classifyFieldCaptureUse can look at
// an identifier's immediate call/selector context without go/ast's own
// Inspect providing parent links directly.
func (b *builder) walkFieldCaptureUses(body ast.Node, byVar map[types.Object][]*fieldCapture, consumed map[*fieldCapture]bool, returnedAt map[types.Object]int, otherUse map[types.Object]bool) {
	if body == nil || len(byVar) == 0 {
		return
	}
	var stack []ast.Node
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if id, ok := n.(*ast.Ident); ok && id.Name != "_" {
			if obj := b.in.Info.Uses[id]; obj != nil {
				if fcs, tracked := byVar[obj]; tracked {
					b.classifyFieldCaptureUse(stack, id, obj, fcs, consumed, returnedAt, otherUse)
				}
			}
		}
		stack = append(stack, n)
		return true
	})
}

// classifyFieldCaptureUse decides what one identifier occurrence of a
// captured variable (id, resolving to obj, tracked by fcs) means for
// resolveFieldCaptures' final verdict:
//
//   - h.otherField or h.otherMethod(...), where otherField/otherMethod
//     doesn't match any of fcs' own field names: an incidental, harmless
//     use of the same variable for something unrelated (e.g. reading a
//     sibling field for a log message) -- neither consumption nor
//     disqualifying, so it is silently ignored.
//   - h.field(...), where field matches a tracked cancel binding's field
//     name and the selector is itself being called: verified consumption
//     -- marks Called and records evidence, the same shape a direct
//     `cancel()` call already gets.
//   - h.field.Add(...)/.Go(...)/.Wait(...), where field matches a tracked
//     group binding's field name: verified worker-accounting/consumption
//     through the field, mirroring observeCall's direct-receiver handling
//     for the same three methods.
//   - h appearing directly as one of a return statement's own result
//     expressions: recorded by position for resolveFieldCaptures to hand
//     to recordReturnedField (the "constructor" shape).
//   - anything else -- the field is referenced but not called, an
//     unrelated method is called on it, h is passed as an argument,
//     reassigned, or used any other way this narrow check does not
//     attempt to follow: marked otherUse, resolveFieldCaptures' signal to
//     fall back to the conservative assume-transferred default rather
//     than guess.
func (b *builder) classifyFieldCaptureUse(stack []ast.Node, id *ast.Ident, obj types.Object, fcs []*fieldCapture, consumed map[*fieldCapture]bool, returnedAt map[types.Object]int, otherUse map[types.Object]bool) {
	parent := ancestor(stack, 1)
	if sel, ok := parent.(*ast.SelectorExpr); ok && sel.X == ast.Expr(id) {
		fieldName := sel.Sel.Name
		matches := false
		for _, fc := range fcs {
			if fc.fieldName == fieldName {
				matches = true
				break
			}
		}
		if !matches {
			return // an unrelated field/method access through the same variable; harmless
		}
		grand := ancestor(stack, 2)
		if call, ok := grand.(*ast.CallExpr); ok && call.Fun == ast.Expr(sel) {
			for _, fc := range fcs {
				if fc.cancel != nil && fc.fieldName == fieldName {
					b.markCancelFieldConsumed(fc, consumed, call)
					return
				}
			}
		}
		if outer, ok := grand.(*ast.SelectorExpr); ok && outer.X == ast.Expr(sel) {
			method := outer.Sel.Name
			if method == "Add" || method == "Go" || method == "Wait" {
				if call, ok := ancestor(stack, 3).(*ast.CallExpr); ok && call.Fun == ast.Expr(outer) {
					for _, fc := range fcs {
						if fc.group != nil && fc.fieldName == fieldName {
							b.markGroupFieldConsumed(fc, method, consumed, call)
							return
						}
					}
				}
			}
		}
		otherUse[obj] = true
		return
	}
	if ret, ok := parent.(*ast.ReturnStmt); ok {
		for i, r := range ret.Results {
			if r == ast.Expr(id) {
				returnedAt[obj] = i
				return
			}
		}
	}
	if as, ok := parent.(*ast.AssignStmt); ok {
		for i, rhs := range as.Rhs {
			if rhs == ast.Expr(id) && pairedLHSIsBlank(as, i) {
				// `_ = h`: the common idiom for explicitly discarding a
				// value (often just to satisfy Go's "declared and not
				// used" rule) -- harmless and not a real use, the same
				// way accessing an unrelated field is.
				return
			}
		}
	}
	otherUse[obj] = true
}

// markCancelFieldConsumed records verified consumption of a cancel
// binding through a struct field: the same Called flag and call-site
// bookkeeping a direct `cancel()` call gets from observeCall, so every
// downstream consumer (LL1001's own check, computeGroupOrdering's stop-
// signal detection) treats the two identically.
func (b *builder) markCancelFieldConsumed(fc *fieldCapture, consumed map[*fieldCapture]bool, call *ast.CallExpr) {
	consumed[fc] = true
	fc.cancel.binding.Called = true
	fc.cancel.callSites = append(fc.cancel.callSites, call)
	fc.cancel.binding.Evidence = append(fc.cancel.binding.Evidence, model.Evidence{
		Kind: "field-consumed", Message: fmt.Sprintf("cancellation function is called through field %q", fc.fieldName), Span: ptrSpan(b.span(call)),
	})
}

// markGroupFieldConsumed records verified worker-accounting or join
// consumption of a group binding through a struct field, mirroring
// observeCall's direct-receiver handling for the same three methods:
// Add/Go increment Starts, Wait sets Joined and records a call site
// (for computeGroupOrdering's own CFG-based ordering checks) the same
// way a direct `wg.Wait()` does. Only Wait marks the capture itself
// resolved (consumed): an Add/Go-only capture still needs a
// consumption/return/other-use verdict for its own sake, since seeing a
// worker started through the field says nothing about whether it is
// ever joined through the field too.
func (b *builder) markGroupFieldConsumed(fc *fieldCapture, method string, consumed map[*fieldCapture]bool, call *ast.CallExpr) {
	switch method {
	case "Add", "Go":
		fc.group.group.Starts++
		fc.group.group.Evidence = append(fc.group.group.Evidence, model.Evidence{Kind: "worker-start", Message: fmt.Sprintf("worker accounting starts here (through field %q)", fc.fieldName), Span: ptrSpan(b.span(call))})
	case "Wait":
		fc.group.group.Joined = true
		fc.group.waitCallSites = append(fc.group.waitCallSites, call)
		fc.group.group.Evidence = append(fc.group.group.Evidence, model.Evidence{Kind: "join", Message: fmt.Sprintf("group is joined here, through field %q", fc.fieldName), Span: ptrSpan(b.span(call))})
		consumed[fc] = true
	}
}

// markFieldCaptureFallback applies this file's ordinary conservative
// assume-transferred default (the same one observeContainerEscape's own
// generic fallback uses) to a capture whose fate resolveFieldCaptures
// could not verify with confidence.
func markFieldCaptureFallback(fc *fieldCapture) {
	if fc.cancel != nil {
		fc.cancel.binding.Escapes = true
		fc.cancel.binding.Evidence = append(fc.cancel.binding.Evidence, model.Evidence{Kind: "ownership-transfer", Message: fmt.Sprintf("cancellation function is stored in field %q; further use is not verified", fc.fieldName)})
	}
	if fc.group != nil {
		fc.group.group.Escapes = true
	}
}

// markFieldCaptureUnconsumed records evidence for a capture confidently
// verified as local and never consumed (resolveFieldCaptures' positive
// "stored struct" leak case), without itself setting Escapes -- leaving
// it false is what lets LL1001/LL1003's own ordinary checks fire.
func markFieldCaptureUnconsumed(fc *fieldCapture) {
	if fc.cancel != nil {
		fc.cancel.binding.Evidence = append(fc.cancel.binding.Evidence, model.Evidence{Kind: "field-not-consumed", Message: fmt.Sprintf("stored in field %q, but that field is never called", fc.fieldName)})
	}
	if fc.group != nil {
		fc.group.group.Evidence = append(fc.group.group.Evidence, model.Evidence{Kind: "field-not-consumed", Message: fmt.Sprintf("stored in field %q, but that field is never joined", fc.fieldName)})
	}
}

// recordReturnedField is the shared landing point for both a capture
// known outright to be returned (an inline literal in a return
// statement) and one resolveFieldCaptures determined is returned via a
// local variable: it registers the constructor shape into
// b.returnFieldInfo (for computeConstructorCallerConsumption to find, on
// the pre-pass call from computeFieldOwnership) and, using whatever
// b.returnFieldConsumption already holds for this binding (populated by
// computeConstructorCallerConsumption before buildFunction's real pass
// runs -- empty on the pre-pass's own call, which is fine, since that
// call's only purpose is populating returnFieldInfo in the first place),
// settles the binding's own Escapes/evidence: verified-consumed-by-a-
// caller and not-yet-verified both apply the same conservative
// assume-transferred fallback (recordReturnedField cannot itself tell
// them apart from any evidence beyond the flag, so both get the same
// safe treatment other than which evidence message is recorded);
// verified as NOT consumed by every checked caller leaves Escapes false,
// so the ordinary LL1001/LL1003 checks fire.
func (b *builder) recordReturnedField(fnObj *types.Func, fc *fieldCapture, resultIndex int) {
	var bindingObj types.Object
	switch {
	case fc.cancel != nil:
		bindingObj = fc.cancel.cancelObj
	case fc.group != nil:
		bindingObj = fc.group.obj
	}
	if bindingObj == nil {
		return
	}
	if fnObj != nil {
		b.returnFieldInfo[bindingObj] = returnFieldSite{fieldName: fc.fieldName, resultIndex: resultIndex, fn: fnObj}
	}
	consumedByCaller, verified := b.returnFieldConsumption[bindingObj]
	if !verified || consumedByCaller {
		markFieldCaptureFallback(fc)
		if verified && consumedByCaller {
			if fc.cancel != nil {
				fc.cancel.binding.Evidence = append(fc.cancel.binding.Evidence, model.Evidence{Kind: "field-consumed", Message: fmt.Sprintf("returned via field %q; a direct caller consumes it", fc.fieldName)})
			}
			if fc.group != nil {
				fc.group.group.Evidence = append(fc.group.group.Evidence, model.Evidence{Kind: "field-consumed", Message: fmt.Sprintf("returned via field %q; a direct caller consumes it", fc.fieldName)})
			}
		}
		return
	}
	// verified, and no checked caller consumes it: a genuine, confirmed
	// leak -- leave Escapes/Called/Joined exactly as constructed (false)
	// and record why.
	if fc.cancel != nil {
		fc.cancel.binding.Evidence = append(fc.cancel.binding.Evidence, model.Evidence{Kind: "field-not-consumed", Message: fmt.Sprintf("returned via field %q; no direct caller is verified to consume it", fc.fieldName)})
	}
	if fc.group != nil {
		fc.group.group.Evidence = append(fc.group.group.Evidence, model.Evidence{Kind: "field-not-consumed", Message: fmt.Sprintf("returned via field %q; no direct caller is verified to consume it", fc.fieldName)})
	}
}

// computeFieldOwnership is the first of two dedicated pre-passes (see
// also computeConstructorCallerConsumption) behind the "constructor-
// returned objects" half of field/constructor ownership tracking
// (docs/roadmap.md item 3): before any function's real diagnostics are
// built, every function is scanned once for the narrow shape this
// capability recognizes -- a cancel/group binding declared in this
// function, stored into a named field of a struct composite literal, and
// returned (either inline in the same return statement, or via a local
// variable that is itself later returned unconsumed) -- populating
// b.returnFieldInfo so computeConstructorCallerConsumption knows which
// functions are "constructors" to check callers of, and buildFunction's
// own later call to the same body-walking machinery
// (observeFunctionBody -> resolveFieldCaptures) can look the answer up
// instead of guessing. This mirrors computeParameterConsumption's own
// reasons for running as an isolated pre-pass rather than being folded
// into buildFunction's main loop: a caller earlier in file order than its
// callee must still see the callee's own returned-field shape. The
// resulting scratch cancels/groups states are discarded, same as
// computeParameterConsumption's own scratch model.Function: this call
// exists only for its side effect on b.returnFieldInfo.
// exportedReturnFieldSites returns, for fnObj, every entry of
// b.returnFieldInfo whose site.fn is fnObj -- i.e. this function's own
// recognized "constructor" fields (computeFieldOwnership's work,
// already fully converged by the time buildFunction runs, same as
// b.paramConsumption) -- in the plain, object-free shape a fact can
// carry. This becomes model.Function.ReturnFieldSites; see
// analyzer.go's fact export and Input.LookupReturnFieldSites.
func (b *builder) exportedReturnFieldSites(fnObj *types.Func) []model.ReturnFieldSite {
	if fnObj == nil || len(b.returnFieldInfo) == 0 {
		return nil
	}
	var out []model.ReturnFieldSite
	for bindingObj, site := range b.returnFieldInfo {
		if site.fn != fnObj {
			continue
		}
		var kind string
		switch {
		case isCancelFuncType(bindingObj.Type()):
			kind = "cancel"
		case groupKind(bindingObj.Type()) != "":
			kind = groupKind(bindingObj.Type())
		default:
			continue
		}
		out = append(out, model.ReturnFieldSite{ResultIndex: site.resultIndex, FieldName: site.fieldName, Kind: kind})
	}
	return out
}

func (b *builder) computeFieldOwnership(source funcSource) {
	fd := source.decl
	if fd.Body == nil {
		return
	}
	contexts := map[types.Object]string{}
	cancels, groups := b.collectBindings(fd, contexts)
	if len(cancels) == 0 && len(groups) == 0 {
		return
	}
	captures, _ := b.collectFieldCaptures(fd.Body, cancels, groups)
	b.resolveFieldCaptures(source.obj, fd.Body, captures)
}

// computeConstructorCallerConsumption is the second of the two
// constructor-return pre-passes: for every function (as a potential
// direct caller), find call sites to a function already known (from
// computeFieldOwnership's b.returnFieldInfo) to return a cancel/group
// binding via a struct field, whose result at the matching position is
// captured by a single named local variable in the caller -- then reuse
// the exact same field-capture consumption search resolveFieldCaptures
// already runs for the "stored struct" pattern (walkFieldCaptureUses),
// this time against the caller's own body, to determine whether that
// specific field is read back and consumed there. Like Phase 5's
// argumentConsumed, this is a single verified hop (the constructor's
// direct, resolvable, same-package, statically-called caller): an
// unresolved or further-indirect caller (the result passed on again, an
// interface method, a different package) leaves b.returnFieldConsumption
// without an entry for that binding, which recordReturnedField's own
// lookup treats as "not verified" and falls back to the conservative
// assume-transferred default, never a leak.
func (b *builder) computeConstructorCallerConsumption(source funcSource) {
	if len(b.returnFieldInfo) == 0 && b.in.LookupReturnFieldSites == nil {
		return
	}
	fd := source.decl
	if fd.Body == nil {
		return
	}
	funcDepth := 0
	var nodeIsFuncLit []bool
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if n == nil {
			last := len(nodeIsFuncLit) - 1
			if last >= 0 {
				if nodeIsFuncLit[last] {
					funcDepth--
				}
				nodeIsFuncLit = nodeIsFuncLit[:last]
			}
			return true
		}
		_, isFuncLit := n.(*ast.FuncLit)
		nodeIsFuncLit = append(nodeIsFuncLit, isFuncLit)
		if isFuncLit {
			funcDepth++
		}
		if funcDepth > 0 {
			return true
		}
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 {
			return true
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		calleeObj, ok := calledObject(call.Fun, b.in.Info).(*types.Func)
		if !ok {
			return true
		}
		matchedLocally := false
		for bindingObj, site := range b.returnFieldInfo {
			if site.fn != calleeObj || site.resultIndex >= len(as.Lhs) {
				continue
			}
			matchedLocally = true
			lhs, ok := as.Lhs[site.resultIndex].(*ast.Ident)
			if !ok || lhs.Name == "_" {
				continue
			}
			varObj := b.in.Info.ObjectOf(lhs)
			if varObj == nil {
				continue
			}
			var kind string
			switch {
			case isCancelFuncType(bindingObj.Type()):
				kind = "cancel"
			case groupKind(bindingObj.Type()) != "":
				kind = groupKind(bindingObj.Type())
			default:
				continue
			}
			b.verifyConstructorCallerField(fd.Body, bindingObj, kind, varObj, site)
		}
		// calleeObj has no same-package returnFieldInfo entry -- either
		// because it isn't a recognized constructor at all, or because
		// its own body lives outside this package. Only the latter is
		// worth a fact lookup; see LookupReturnFieldSites' own doc
		// comment for what this can and can't achieve for that case.
		if !matchedLocally && b.funcs[calleeObj] == nil && b.in.LookupReturnFieldSites != nil {
			if sites, ok := b.in.LookupReturnFieldSites(calleeObj); ok {
				for _, fs := range sites {
					if fs.ResultIndex >= len(as.Lhs) {
						continue
					}
					lhs, ok := as.Lhs[fs.ResultIndex].(*ast.Ident)
					if !ok || lhs.Name == "_" {
						continue
					}
					varObj := b.in.Info.ObjectOf(lhs)
					if varObj == nil {
						continue
					}
					b.verifyConstructorCallerField(fd.Body, calleeObj, fs.Kind, varObj, returnFieldSite{fieldName: fs.FieldName, resultIndex: fs.ResultIndex, fn: calleeObj})
				}
			}
		}
		return true
	})
}

// verifyConstructorCallerField checks one specific call site's captured
// result variable (varObj, expected to hold site.fieldName's binding)
// against the calling function's own body, via the same
// walkFieldCaptureUses machinery a locally-declared "stored struct"
// capture is resolved with. kind ("cancel", "waitgroup", or "errgroup")
// tells cancel and group bindings apart -- for a same-package
// constructor this is derived from bindingObj's own static type (see
// this function's caller in computeConstructorCallerConsumption), and
// for a cross-package one it comes directly from the imported fact
// (Input.LookupReturnFieldSites), which is exactly why this takes a
// plain kind string rather than deriving it here: a cross-package
// binding has no real types.Object in this build to derive it from.
// bindingObj itself remains an opaque identity used only as this
// function's own map key (see setReturnFieldConsumption) and as the
// synthetic cancelState/groupState's own field -- neither
// classifyFieldCaptureUse nor walkFieldCaptureUses ever reads its
// identity for anything beyond that, only fc.cancel/fc.group's presence
// and fc.fieldName.
func (b *builder) verifyConstructorCallerField(callerBody *ast.BlockStmt, bindingObj types.Object, kind string, varObj types.Object, site returnFieldSite) {
	fc := &fieldCapture{varObj: varObj, fieldName: site.fieldName, returnIndex: -1}
	switch kind {
	case "cancel":
		fc.cancel = &cancelState{cancelObj: bindingObj}
	case "waitgroup", "errgroup":
		fc.group = &groupState{obj: bindingObj}
	default:
		return
	}
	consumed := map[*fieldCapture]bool{}
	returnedAt := map[types.Object]int{}
	otherUse := map[types.Object]bool{}
	byVar := map[types.Object][]*fieldCapture{varObj: {fc}}
	b.walkFieldCaptureUses(callerBody, byVar, consumed, returnedAt, otherUse)
	_, passedOnward := returnedAt[varObj]
	switch {
	case consumed[fc]:
		b.setReturnFieldConsumption(bindingObj, true)
	case otherUse[varObj]:
		// Can't verify at this call site either way (e.g. the caller
		// passes the handle on somewhere this check does not follow) --
		// leave it unresolved unless a different call site already
		// resolved it.
	case passedOnward:
		// The caller itself just returns the handle onward -- a second
		// hop, out of scope for this single verified hop (matching Phase
		// 5's own one-hop guarantee for direct parameter passing); leave
		// it unresolved.
	default:
		b.setReturnFieldConsumption(bindingObj, false)
	}
}

// setReturnFieldConsumption merges one call site's verdict into
// b.returnFieldConsumption for bindingObj: a verified consumption is
// permanent and wins over any other call site's verdict (recorded
// unconditionally, even overwriting an earlier false), while a verified
// non-consumption only takes effect if no entry exists yet, so that one
// consuming caller is always enough to clear a binding even if another,
// earlier-checked caller drops it -- consistent with this file's general
// preference for avoiding a false positive over catching every leak.
func (b *builder) setReturnFieldConsumption(obj types.Object, consumed bool) {
	if consumed {
		b.returnFieldConsumption[obj] = true
		return
	}
	if _, already := b.returnFieldConsumption[obj]; !already {
		b.returnFieldConsumption[obj] = false
	}
}

func (b *builder) markChildUses(call *ast.CallExpr, cancels []*cancelState) {
	used := objectsUsed(call, b.in.Info, false)
	for _, c := range cancels {
		if c.ctxObj != nil && hasObject(used, c.ctxObj) {
			c.binding.UsedByChild = true
			c.binding.Evidence = append(c.binding.Evidence, model.Evidence{Kind: "child-use", Message: "derived context is used by a child goroutine", Span: ptrSpan(b.span(call))})
		}
	}
}

// groupBalance is the literal, provable Add/Done tally
// walkGroupBalance/walkGroupBalanceStmts produces for one sync.WaitGroup,
// for exactly the statements it was computed over -- either a whole
// function body, or (see foldConditionalArms) one isolated, mutually
// exclusive arm of a conditional. See computeGroupBalances.
type groupBalance struct {
	addTotal, doneTotal int
	fullyKnown          bool
}

// loopScope accumulates same-idiom Add(1)/spawned-Done counts found
// directly within one loop's own body (a nested loop gets its own separate
// scope, closed and reconciled independently before its results, if any,
// propagate up as ordinary otherActivity), so they can be compared once
// that loop's body has been fully walked. See closeLoopScopeInto.
type loopScope struct {
	addOnes      int
	spawnedDones int
	// otherActivity is set by anything found inside this loop that doesn't
	// fit the narrow "some number of Add(1) calls matched by the same
	// number of go-statements whose spawned body calls Done()" idiom: a
	// non-literal or non-1 Add amount, a bare Done() call with no
	// associated spawn, a conditional arm inside the loop that doesn't
	// itself net to zero (see foldConditionalArms), or (via
	// closeLoopScopeInto) a raw addOnes/spawnedDones count that doesn't
	// match.
	otherActivity bool
}

// computeGroupBalances fills in CountMismatch for every local WaitGroup in
// groups (Phase 6, "count intervals" and "common Add/Done relationships",
// docs/cfg-migration-plan.md): a second, narrower pass over the same
// function body specifically for WaitGroup accounting, kept separate from
// observeCall's single-node-at-a-time traversal because it needs
// same-block/same-loop/same-branch sibling context that traversal doesn't
// carry. errgroup.Group is skipped entirely: its Add/Done-equivalent
// accounting is internal to the library, so there is nothing here for a
// caller to get wrong the same way.
//
// See walkGroupBalance, foldLoopBody, and foldConditionalArms for exactly
// what is and isn't recognized; CountMismatch is set only from a positive
// literal proof of imbalance, never from an inability to fully account for
// every call site.
func (b *builder) computeGroupBalances(groups []*groupState, body *ast.BlockStmt, info *types.Info) {
	for _, g := range groups {
		if g.group.Kind != "waitgroup" || g.obj == nil {
			continue
		}
		bal := b.walkGroupBalance(body, g.obj, info)
		if bal.fullyKnown && bal.addTotal > bal.doneTotal {
			g.group.CountMismatch = true
			g.group.Evidence = append(g.group.Evidence, model.Evidence{
				Kind:    "count-mismatch",
				Message: fmt.Sprintf("literal accounting shows %d more Add than Done; Wait may never return", bal.addTotal-bal.doneTotal),
			})
		}
	}
}

// walkGroupBalance computes obj's own Add()/Done() accounting for the
// whole of body. See walkGroupBalanceStmt for exactly what is and isn't
// recognized in each statement.
func (b *builder) walkGroupBalance(body *ast.BlockStmt, obj types.Object, info *types.Info) groupBalance {
	if body == nil {
		return groupBalance{fullyKnown: true}
	}
	return b.walkGroupBalanceStmts(body.List, obj, info)
}

// walkGroupBalanceStmts computes a fresh, self-contained groupBalance for
// exactly the statements in list, executed unconditionally in the
// sequence given -- used both for a whole function body and, recursively,
// for one isolated arm of a conditional (see foldConditionalArms), which
// is what makes it safe to call on an arm without that arm's own
// accounting bleeding into a sibling arm's.
func (b *builder) walkGroupBalanceStmts(list []ast.Stmt, obj types.Object, info *types.Info) groupBalance {
	bal := groupBalance{fullyKnown: true}
	for _, s := range list {
		b.walkGroupBalanceStmt(s, obj, info, &bal, nil)
	}
	return bal
}

// walkGroupBalanceStmt processes one statement's contribution to obj's
// running Add/Done tally: either bal directly (the enclosing function
// body or conditional arm's own total), or, when scope is non-nil,
// that loop's own loopScope (see loopScope's own doc comment for why a
// loop-scoped site is tracked separately rather than added to bal
// directly -- its true per-run contribution depends on an iteration count
// this analysis does not track, unless it matches the recognized
// Add(1)-paired-with-a-spawned-Done idiom, which balances regardless of
// how many times the loop actually runs).
//
// A `go` statement counts as a spawned Done site the same way in either
// position: an inline closure that calls Done() on obj directly
// (bodyCallsMethodOn), or a call to a resolvable same-package function
// that receives obj as a direct argument and calls Done() on the
// corresponding parameter itself (calleeDoneParamMatches -- the
// named-function counterpart, for the equally common `wg.Add(1); go
// worker(&wg)` idiom).
//
// A conditional (if/switch/type-switch/select) is walked differently from
// either: each of its arms is a mutually exclusive alternative -- exactly
// one of them runs, never more than one -- so each gets its own fresh,
// isolated groupBalance (walkGroupBalanceStmts on just that arm's own
// statement list), and the combined result only folds into bal/scope when
// that's safe regardless of which arm actually runs (foldConditionalArms).
// This is what stops an Add() in one arm and an unrelated Done() in a
// sibling arm from ever being treated as balancing each other, the way a
// single shared running total would.
func (b *builder) walkGroupBalanceStmt(s ast.Stmt, obj types.Object, info *types.Info, bal *groupBalance, scope *loopScope) {
	if call, ok := groupMethodCall(s, obj, info); ok {
		switch selectorMethod(call.Fun) {
		case "Add":
			amount, literal := literalNonNegativeInt(soleArg(call), info)
			noteAddInto(bal, scope, amount, literal)
		case "Done":
			noteDoneInto(bal, scope)
		}
		return
	}
	if goStmt, ok := s.(*ast.GoStmt); ok {
		if lit, ok := goStmt.Call.Fun.(*ast.FuncLit); ok {
			if bodyCallsMethodOn(lit.Body, obj, "Done", info) {
				noteSpawnedDoneInto(bal, scope)
			}
			return
		}
		if b.calleeDoneParamMatches(goStmt.Call, obj) {
			noteSpawnedDoneInto(bal, scope)
		}
		return
	}
	switch x := s.(type) {
	case *ast.BlockStmt:
		for _, sub := range x.List {
			b.walkGroupBalanceStmt(sub, obj, info, bal, scope)
		}
	case *ast.IfStmt:
		arms := []groupBalance{b.walkGroupBalanceStmts(x.Body.List, obj, info)}
		if x.Else != nil {
			// x.Else is either another *ast.BlockStmt or (for an else-if
			// chain) a nested *ast.IfStmt; wrapping it as a one-statement
			// list and recursing through walkGroupBalanceStmt handles
			// both uniformly, including arbitrarily long else-if chains.
			arms = append(arms, b.walkGroupBalanceStmts([]ast.Stmt{x.Else}, obj, info))
		} else {
			arms = append(arms, groupBalance{fullyKnown: true}) // implicit empty else: "if" not taken
		}
		foldConditionalArms(bal, scope, arms)
	case *ast.ForStmt:
		b.foldLoopBody(x.Body, obj, info, bal)
	case *ast.RangeStmt:
		b.foldLoopBody(x.Body, obj, info, bal)
	case *ast.SwitchStmt:
		foldConditionalArms(bal, scope, b.walkCaseClauses(x.Body.List, obj, info))
	case *ast.TypeSwitchStmt:
		foldConditionalArms(bal, scope, b.walkCaseClauses(x.Body.List, obj, info))
	case *ast.SelectStmt:
		foldConditionalArms(bal, scope, b.walkCommClauses(x.Body.List, obj, info))
	case *ast.LabeledStmt:
		b.walkGroupBalanceStmt(x.Stmt, obj, info, bal, scope)
	}
}

// walkCaseClauses computes one isolated groupBalance per case of a
// switch/type-switch, for foldConditionalArms. If none of the clauses is
// `default:`, "no case matches" is itself a possible outcome (falling
// through to whatever comes after the switch untouched), so an implicit
// empty arm is added for it -- the switch/type-switch counterpart to an
// `if` with no `else`.
func (b *builder) walkCaseClauses(list []ast.Stmt, obj types.Object, info *types.Info) []groupBalance {
	var arms []groupBalance
	hasDefault := false
	for _, c := range list {
		cc, ok := c.(*ast.CaseClause)
		if !ok {
			continue
		}
		arms = append(arms, b.walkGroupBalanceStmts(cc.Body, obj, info))
		if cc.List == nil { // nil List is how a `default:` clause is represented
			hasDefault = true
		}
	}
	if !hasDefault {
		arms = append(arms, groupBalance{fullyKnown: true})
	}
	return arms
}

// walkCommClauses computes one isolated groupBalance per case of a
// select, for foldConditionalArms. Unlike a switch, a select (with or
// without a `default:`) always executes exactly one of its own clauses --
// blocking until one is ready if it has no default -- so there is no
// "none of them ran" case to add an implicit arm for.
func (b *builder) walkCommClauses(list []ast.Stmt, obj types.Object, info *types.Info) []groupBalance {
	var arms []groupBalance
	for _, c := range list {
		cc, ok := c.(*ast.CommClause)
		if !ok {
			continue
		}
		arms = append(arms, b.walkGroupBalanceStmts(cc.Body, obj, info))
	}
	if len(arms) == 0 {
		arms = append(arms, groupBalance{fullyKnown: true})
	}
	return arms
}

// noteAddInto, noteDoneInto, and noteSpawnedDoneInto route one Add/Done/
// spawned-Done site into either bal (scope == nil) or scope (scope !=
// nil), matching walkGroupBalanceStmt's own destination for whichever
// statement it just processed.
func noteAddInto(bal *groupBalance, scope *loopScope, amount int, literal bool) {
	switch {
	case scope == nil && literal:
		bal.addTotal += amount
	case scope != nil && literal && amount == 1:
		scope.addOnes++
	case scope != nil:
		scope.otherActivity = true
	default:
		bal.fullyKnown = false
	}
}

func noteDoneInto(bal *groupBalance, scope *loopScope) {
	if scope == nil {
		bal.doneTotal++
		return
	}
	scope.otherActivity = true // a bare Done() inside a loop, on its own, isn't the recognized idiom
}

func noteSpawnedDoneInto(bal *groupBalance, scope *loopScope) {
	if scope == nil {
		bal.doneTotal++
		return
	}
	scope.spawnedDones++
}

// foldLoopBody walks one loop's own body into a fresh loopScope and folds
// the result into bal once the whole body has been processed -- always
// directly into bal, regardless of whether this loop is itself nested
// inside another loop or a conditional arm: an unresolved nested loop
// bails all the way out rather than being absorbed into an enclosing
// loop's own idiom-matching, which would conflate two different loops'
// iteration counts.
func (b *builder) foldLoopBody(body *ast.BlockStmt, obj types.Object, info *types.Info, bal *groupBalance) {
	if body == nil {
		return
	}
	inner := &loopScope{}
	for _, s := range body.List {
		b.walkGroupBalanceStmt(s, obj, info, bal, inner)
	}
	closeLoopScopeInto(bal, inner)
}

// closeLoopScopeInto reconciles one loop's own accumulated loopScope once
// its body has been fully walked, folding the result into bal.
func closeLoopScopeInto(bal *groupBalance, scope *loopScope) {
	if scope.otherActivity {
		bal.fullyKnown = false
		return
	}
	if scope.addOnes != scope.spawnedDones {
		// Both zero means no activity for this group in this loop at all
		// -- fine, nothing to reconcile. A nonzero mismatch (e.g. two
		// Add(1) sites but only one spawned Done per iteration) is a real
		// shape this narrow idiom check can't resolve into a specific
		// number without knowing the iteration count, so it falls back to
		// "not fully known" rather than guessing.
		if scope.addOnes != 0 || scope.spawnedDones != 0 {
			bal.fullyKnown = false
		}
		return
	}
	// addOnes == spawnedDones (including both zero): this loop balances
	// itself every iteration regardless of how many iterations actually
	// run. Nothing propagates to addTotal/doneTotal, and fullyKnown is
	// unaffected.
}

// foldConditionalArms folds the results of walking each of a
// conditional's mutually exclusive arms into bal, or scope if non-nil.
// Safe only when every arm's own net delta (addTotal-doneTotal) is
// exactly zero and every arm is itself fully known: whichever arm
// actually runs at runtime, the net change to the group's counter is zero
// either way, so nothing needs to be added to the running total. This is
// what lets the common conditionally-started worker idiom (`if needed {
// wg.Add(1); go worker() }`, no else) stay silent rather than falsely
// balanced or falsely flagged: that arm's own delta is zero (one Add
// matched by one spawned Done within the same arm), and the implicit
// empty else is trivially zero too.
//
// An arm with a nonzero delta, or one that isn't itself fully known,
// makes the combined result "not fully known" for whatever it's folded
// into: which arm will actually run isn't something this analysis tracks,
// so two sibling arms' nonzero deltas can never be safely combined the
// way sequential statements can. This is also what stops an Add() in one
// arm and an unrelated Done() in a sibling arm from ever being treated as
// balancing each other -- previously, both were walked into the very same
// running total regardless of which branch they were in, so a bare
// Done() in an "else" that has nothing to do with an Add() over in the
// "if" could look balanced on paper while being a guaranteed negative-
// counter panic on whichever runs at runtime.
func foldConditionalArms(bal *groupBalance, scope *loopScope, arms []groupBalance) {
	safe := true
	for _, arm := range arms {
		if !arm.fullyKnown || arm.addTotal != arm.doneTotal {
			safe = false
			break
		}
	}
	if safe {
		return // every arm nets to zero; nothing to add anywhere
	}
	if scope != nil {
		scope.otherActivity = true
		return
	}
	bal.fullyKnown = false
}

// groupMethodCall reports whether s is (or directly defers) a call to
// obj's Add or Done method, covering both `wg.Done()` and `defer
// wg.Done()` the same way -- a bare Done() call site directly in the
// owner's own body, with no associated spawn, is unusual but not
// impossible, and undercounting it would risk a false CountMismatch, not
// just a missed one.
func groupMethodCall(s ast.Stmt, obj types.Object, info *types.Info) (*ast.CallExpr, bool) {
	var call *ast.CallExpr
	switch x := s.(type) {
	case *ast.ExprStmt:
		call, _ = x.X.(*ast.CallExpr)
	case *ast.DeferStmt:
		call = x.Call
	}
	if call == nil || selectorReceiverObject(call.Fun, info) != obj {
		return nil, false
	}
	switch selectorMethod(call.Fun) {
	case "Add", "Done":
		return call, true
	}
	return nil, false
}

// soleArg returns call's only argument, or nil if it doesn't have exactly
// one (sync.WaitGroup.Add always does in valid, type-checked code; nil
// here just means literalNonNegativeInt will report "not a literal").
func soleArg(call *ast.CallExpr) ast.Expr {
	if len(call.Args) != 1 {
		return nil
	}
	return call.Args[0]
}

// literalNonNegativeInt reports the constant-folded, non-negative integer
// value of e, using the type-checker's own constant evaluation
// (info.Types[e].Value) rather than hand-rolling *ast.BasicLit matching --
// this is what lets a named constant (`const workers = 4; wg.Add(workers)`)
// count exactly the same way a bare literal does.
func literalNonNegativeInt(e ast.Expr, info *types.Info) (int, bool) {
	if e == nil {
		return 0, false
	}
	tv, ok := info.Types[e]
	if !ok || tv.Value == nil {
		return 0, false
	}
	n, ok := constant.Int64Val(tv.Value)
	if !ok || n < 0 || n > math.MaxInt32 {
		return 0, false
	}
	return int(n), true
}

// calleeDoneParamMatches reports whether call's target is a resolvable
// function -- same-package (via b.paramDoneCalled, computeParamDoneCalled's
// own fixed point) or, for a callee outside the current package, via a
// versioned fact (Input.LookupParamDoneCalled) -- that receives obj as a
// direct argument at some position, and whose own body eventually calls
// Done() on that corresponding parameter, to any depth: the named-
// function counterpart to bodyCallsMethodOn's closure-capture check
// above, for the equally common `wg.Add(1); go worker(&wg)` idiom, where
// worker's whole job (however many further named helpers it delegates
// to) is to call Done() on whatever it's given. This deliberately does
// not reuse b.paramConsumption (Phase 5's own fixed point for cancel/group
// parameters): that answers a different question -- does the parameter
// get Wait()ed or further transferred -- appropriate for verifying an
// ownership handoff, not for a worker whose job is specifically to
// decrement the counter its caller already incremented.
func (b *builder) calleeDoneParamMatches(call *ast.CallExpr, obj types.Object) bool {
	funcObj, ok := calledObject(call.Fun, b.in.Info).(*types.Func)
	if !ok {
		return false
	}
	index := -1
	for i, arg := range call.Args {
		if identObject(arg, b.in.Info) == obj {
			index = i
			break
		}
	}
	if index == -1 {
		return false
	}
	if decl := b.funcs[funcObj]; decl != nil {
		paramObj := paramObjectAtIndex(b.in.Info, decl, index)
		if paramObj == nil {
			return false
		}
		return b.paramDoneCalled[paramObj]
	}
	if b.in.LookupParamDoneCalled != nil {
		if called, ok := b.in.LookupParamDoneCalled(funcObj, index); ok {
			return called
		}
	}
	return false
}

// computeParamDoneCalled (re)computes, for every sync.WaitGroup-typed
// parameter of fd, whether Done() is eventually called on it: directly
// in fd's own body (bodyCallsMethodOn), or via fd passing it on, as a
// direct argument to an ordinary call or a go-statement's call, to a
// further resolvable same-package function whose own corresponding
// parameter -- per this same fixed point, one step further along --
// already eventually calls Done() (bodyDelegatesDone). Build calls this
// once per function per sweep, in the same loop as
// computeParameterConsumption and for the identical structural reason: a
// chain of several named helpers (`worker(wg){ helper(wg) }`,
// `helper(wg){ wg.Done() }`) needs helper's own answer known before
// worker's can be, regardless of which order they happen to be declared
// in. Unlike computeParameterConsumption, this needs no "pending"
// signal: recordParamDoneCalled's OR-merge means a not-yet-true entry
// mid-sweep is just today's correct answer, not a wrong one standing in
// for a right one, and later sweeps can only ever raise it, never need to
// retract it.
func (b *builder) computeParamDoneCalled(fd *ast.FuncDecl) (reportChanged bool) {
	if fd.Type.Params == nil || fd.Body == nil {
		return false
	}
	var groupParams []types.Object
	for _, field := range fd.Type.Params.List {
		for _, name := range field.Names {
			obj := b.in.Info.ObjectOf(name)
			if obj != nil && name.Name != "_" && groupKind(obj.Type()) == "waitgroup" {
				groupParams = append(groupParams, obj)
			}
		}
	}
	if len(groupParams) == 0 {
		return false
	}
	for _, obj := range groupParams {
		next := bodyCallsMethodOn(fd.Body, obj, "Done", b.in.Info) || b.bodyDelegatesDone(fd.Body, obj)
		if b.recordParamDoneCalled(obj, next) {
			reportChanged = true
		}
	}
	return reportChanged
}

// recordParamDoneCalled merges next into b.paramDoneCalled[obj] via OR
// (see the field's own doc comment for why no "pending"/existed
// distinction is needed here, unlike recordParamConsumption) and reports
// whether the recorded value changed, which is what tells Build's sweep
// loop whether another pass is needed.
func (b *builder) recordParamDoneCalled(obj types.Object, next bool) bool {
	prev := b.paramDoneCalled[obj]
	if next && !prev {
		b.paramDoneCalled[obj] = true
		return true
	}
	return false
}

// bodyDelegatesDone reports whether body passes obj as a direct argument,
// at some call site (an ordinary call or a go-statement's call, but not
// one reached only through a further-nested function literal -- the same
// scoping bodyCallsMethodOn already uses), to a resolvable same-package
// function whose own corresponding parameter is, per
// computeParamDoneCalled's own fixed point, already known to eventually
// call Done(). Existence-only, like bodyCallsMethodOn: it does not
// establish this happens on every path, only that it appears somewhere.
func (b *builder) bodyDelegatesDone(body ast.Node, obj types.Object) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		var call *ast.CallExpr
		switch x := n.(type) {
		case *ast.CallExpr:
			call = x
		case *ast.GoStmt:
			call = x.Call
		default:
			return true
		}
		funcObj, ok := calledObject(call.Fun, b.in.Info).(*types.Func)
		if !ok {
			return true
		}
		decl := b.funcs[funcObj]
		if decl == nil {
			return true
		}
		index := -1
		for i, arg := range call.Args {
			if identObject(arg, b.in.Info) == obj {
				index = i
				break
			}
		}
		if index == -1 {
			return true
		}
		paramObj := paramObjectAtIndex(b.in.Info, decl, index)
		if paramObj != nil && b.paramDoneCalled[paramObj] {
			found = true
			return false
		}
		return true
	})
	return found
}

// bodyCallsMethodOn reports whether body contains a call obj.method(...)
// anywhere, including one reached via defer, except inside a further-
// nested function literal (consistent with the rest of this file's
// scoping): an existence check, not a reachability one -- it does not
// establish the call happens on every path through body, only that it
// appears somewhere in it. See walkGroupBalance's own doc comment for why
// that narrower guarantee is what this idiom check needs.
func bodyCallsMethodOn(body ast.Node, obj types.Object, method string, info *types.Info) bool {
	if body == nil || obj == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok && selectorReceiverObject(call.Fun, info) == obj && selectorMethod(call.Fun) == method {
			found = true
			return false
		}
		return true
	})
	return found
}

// deferredCallSet returns the set of call expressions in body (not
// descending into a nested FuncLit, consistent with
// collectStopWrapperCalls) that are the direct target of a defer
// statement. computeGroupOrdering uses this to recognize a cancel
// binding whose stop signal is only ever sent via defer: internal/cfg
// records a defer at the defer statement's own lexical position, not at
// the function's actual return time (see cfg.go's handling of
// *ast.DeferStmt), which is misleading for an ordering question like
// stop-before-wait -- a deferred call always actually runs when the
// enclosing function is about to return, which is unconditionally after
// any statement, including a Wait() call, that already ran earlier in
// the same invocation. See allCallsDeferred and computeGroupOrdering's
// own doc comment for how this is used.
func deferredCallSet(body *ast.BlockStmt) map[*ast.CallExpr]bool {
	set := map[*ast.CallExpr]bool{}
	if body == nil {
		return set
	}
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if d, ok := n.(*ast.DeferStmt); ok {
			set[d.Call] = true
		}
		return true
	})
	return set
}

// allCallsDeferred reports whether every call in calls is present in
// deferred -- i.e. every registration of this stop signal is via defer,
// so the signal never actually fires until the enclosing function is
// already on its way out. A binding with no call sites at all is not
// "all deferred" (there is nothing to have proven anything about); see
// computeGroupOrdering.
func allCallsDeferred(calls []*ast.CallExpr, deferred map[*ast.CallExpr]bool) bool {
	if len(calls) == 0 {
		return false
	}
	for _, call := range calls {
		if !deferred[call] {
			return false
		}
	}
	return true
}

// collectStopWrapperCalls finds every call in body (not descending into a
// nested FuncLit) that resolves to a configured stop_wrapper -- the same
// recognition internal/cfg's trusted-stop edges use (b.trustedTerminator),
// exposed here as plain call sites for computeGroupOrdering's ordering
// check (Phase 3 completion, "stop-before-wait",
// docs/cfg-migration-plan.md). A cancel-function call is a separate kind
// of stop signal and is collected on cancelState.callSites directly by
// observeCall instead, since a cancel binding already tracks its own calls
// for LL1001's own purposes.
func (b *builder) collectStopWrapperCalls(body *ast.BlockStmt) []*ast.CallExpr {
	var calls []*ast.CallExpr
	if body == nil {
		return calls
	}
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok && b.hasWrapper(b.stopWrappers, b.callName(call)) {
			calls = append(calls, call)
		}
		return true
	})
	return calls
}

// callSitesOf resolves each call in calls to the CallSite (from
// internal/cfg.Build) it landed in, skipping any call Build never saw in a
// direct effect position (see Build's own doc comment for exactly which
// positions those are).
func callSitesOf(calls []*ast.CallExpr, callSites map[*ast.CallExpr]flowgraph.CallSite) []flowgraph.CallSite {
	var sites []flowgraph.CallSite
	for _, call := range calls {
		if site, ok := callSites[call]; ok {
			sites = append(sites, site)
		}
	}
	return sites
}

func blockSetOf(sites []flowgraph.CallSite) map[model.BlockID]bool {
	set := map[model.BlockID]bool{}
	for _, site := range sites {
		set[site.Block] = true
	}
	return set
}

// stopProvenAfterWait reports whether stop is guaranteed to be reached, on
// every path from g's entry, only after at least one of waitSites has
// already run. When stop shares a block with a wait site, this is decided
// directly by comparing in-block instruction index -- see
// flowgraph.CallSite's own doc comment for why that is both necessary and
// sufficient for that case, which model.CFG.ReachableAvoiding alone can't
// resolve (a block is that function's own avoid-set target, and a block
// can't avoid containing itself). Otherwise it falls back to
// ReachableAvoiding at block granularity: stop is proven-after iff its
// block cannot be reached from entry without passing through some wait
// site's own block first.
func stopProvenAfterWait(g *model.CFG, waitSites []flowgraph.CallSite, waitBlocks map[model.BlockID]bool, reachableAvoidingWaits map[model.BlockID]bool, stop flowgraph.CallSite) bool {
	if waitBlocks[stop.Block] {
		for _, w := range waitSites {
			if w.Block == stop.Block && w.Index < stop.Index {
				return true
			}
		}
		return false
	}
	return !reachableAvoidingWaits[stop.Block]
}

// computeGroupOrdering fills in JoinedOnAllPaths and StopAfterWait for
// every local group in groups, using the owner function's own CFG and the
// call-site map internal/cfg.Build produced alongside it (Phase 3
// completion, "join-before-owner-return" and "stop-before-wait",
// docs/cfg-migration-plan.md). g may be nil (a function with no body to
// build a CFG from); every group's fields are then left untouched at
// their safe defaults (JoinedOnAllPaths nil, StopAfterWait at its zero
// value of false).
//
// A candidate "stop signal" is any call to a configured stop_wrapper
// (unconditionally trusted, the same way internal/cfg's own trusted-stop
// edges already trust one), or a call to the cancel function of a tracked
// context that is both actually called (binding.Called) and actually
// observed to be captured by some goroutine started in this same function
// (binding.UsedByChild) -- requiring UsedByChild specifically excludes an
// unrelated cancel() call whose context no goroutine here even looks at,
// which meaningfully narrows, though does not eliminate, the residual risk
// of crediting a stop signal that happens to belong to a *different*
// worker group than the one being checked when a function manages more
// than one. That narrower per-group correlation is not attempted here;
// see docs/limitations.md.
//
// A cancel binding whose *every* call site is a deferred one
// (allCallsDeferred) is handled separately from stopProvenAfterWait's
// ordinary block/index comparison: since such a signal never actually
// fires until the enclosing function is already returning, it is
// unconditionally sent after any Wait() call that already ran earlier in
// the same invocation, regardless of where internal/cfg happened to
// record the defer statement's own call site. This is deliberately an
// all-or-nothing test on the *binding*, not a per-call one: a binding
// that has at least one non-deferred call site (e.g. `defer cancel()` as
// a panic/early-return safety net alongside an explicit `cancel()` right
// before Wait(), as in examples/stop_before_wait) already has a real
// escape hatch that may independently prove the signal is sent before
// Wait(), so it is left to the ordinary per-call-site loop below rather
// than short-circuited here -- folding a defer-only binding into that
// same loop would make its (misleadingly early) recorded position count
// as "not proven after," silently defeating the very check this exists
// to fix. As with the stop-signal candidacy check above, this is decided
// per binding, not per group; see docs/limitations.md.
//
// The identical all-or-nothing test also applies to configured
// stop_wrapper calls (collectStopWrapperCalls), treated as one flat
// group the same way the rest of this function already treats them
// (undifferentiated by which specific wrapper function produced each
// call, matching the existing per-group correlation limitation noted
// above): a stop_wrapper is exactly as capable of being called only via
// `defer stopFn()` as a cancel function is, and internal/cfg's defer
// mispositioning is not specific to context.CancelFunc values -- it
// applies to any deferred call. This was originally missed when the
// cancel-binding case above was fixed: confirmed concretely,
// `defer Shutdown()` (Shutdown configured as a stop_wrapper) at the top
// of a function, with no other call to it, deadlocks exactly like the
// equivalent `defer cancel()` case, for the identical reason, but was
// not caught until this was specifically checked for.
func (b *builder) computeGroupOrdering(groups []*groupState, cancels []*cancelState, body *ast.BlockStmt, g *model.CFG, callSites map[*ast.CallExpr]flowgraph.CallSite) {
	if g == nil || len(groups) == 0 {
		return
	}
	deferredCalls := deferredCallSet(body)
	var stopSignals []*ast.CallExpr
	deferOnlyStopSeen := false
	for _, c := range cancels {
		if c.binding.Called && c.binding.UsedByChild {
			stopSignals = append(stopSignals, c.callSites...)
			if allCallsDeferred(c.callSites, deferredCalls) {
				deferOnlyStopSeen = true
			}
		}
	}
	stopWrapperCalls := b.collectStopWrapperCalls(body)
	stopSignals = append(stopSignals, stopWrapperCalls...)
	if allCallsDeferred(stopWrapperCalls, deferredCalls) {
		deferOnlyStopSeen = true
	}
	stopSites := callSitesOf(stopSignals, callSites)

	for _, gr := range groups {
		if !gr.group.Joined {
			continue // LL1003/LL1004 already fire on this; nothing further to establish
		}
		waitSites := callSitesOf(gr.waitCallSites, callSites)
		if len(waitSites) == 0 {
			continue // Joined came from a join_wrapper call, not a direct Wait(); no call site here to find a block for
		}
		waitBlocks := blockSetOf(waitSites)
		reachableAvoidingWaits := g.ReachableAvoiding(g.Entry, waitBlocks)
		onAllPaths := !reachableAvoidingWaits[g.Exit]
		gr.group.JoinedOnAllPaths = &onAllPaths
		if !onAllPaths {
			gr.group.Evidence = append(gr.group.Evidence, model.Evidence{Kind: "join-not-on-all-paths", Message: "some path from the function's entry to its return bypasses every Wait() call for this group"})
		}
		if deferOnlyStopSeen {
			gr.group.StopAfterWait = true
			gr.group.Evidence = append(gr.group.Evidence, model.Evidence{Kind: "stop-after-wait", Message: "the worker stop signal is only ever sent via a deferred call, which does not run until the function is already returning -- after this Wait() call"})
			continue
		}
		for _, stop := range stopSites {
			if stopProvenAfterWait(g, waitSites, waitBlocks, reachableAvoidingWaits, stop) {
				gr.group.StopAfterWait = true
				gr.group.Evidence = append(gr.group.Evidence, model.Evidence{Kind: "stop-after-wait", Message: "the worker stop signal is only reachable after this Wait() call"})
				break
			}
		}
	}
}

func (b *builder) buildGoroutine(site ast.Node, call *ast.CallExpr, callerContexts map[types.Object]string, kind string) model.Goroutine {
	siteSpan := b.span(site)
	if lit, ok := call.Fun.(*ast.FuncLit); ok {
		return b.analyzeLifecycleBody(lit.Body, callerContexts, kind, siteSpan)
	}

	obj, _ := calledObject(call.Fun, b.in.Info).(*types.Func)
	if obj == nil {
		return model.Goroutine{Span: siteSpan, Kind: kind, Evidence: []model.Evidence{{Kind: "unsupported", Message: "goroutine target body is not statically identifiable"}}}
	}
	if decl := b.funcs[obj]; decl != nil {
		if !b.analyzed[obj] {
			return model.Goroutine{Span: siteSpan, Kind: kind, Evidence: []model.Evidence{{Kind: "unsupported", Message: "same-package goroutine target lies beyond max_functions bound"}}}
		}
		g := b.functionSummary(obj, decl)
		g.Span = siteSpan
		g.Kind = kind
		g.Evidence = append(g.Evidence, model.Evidence{Kind: "direct-callee", Message: "same-package goroutine target inspected: " + obj.FullName(), Span: ptrSpan(b.span(decl))})
		return g
	}
	if b.in.LookupFunctionSummary != nil {
		if imported, ok := b.in.LookupFunctionSummary(obj); ok {
			g := cloneGoroutine(imported)
			g.Span = siteSpan
			g.Kind = kind
			// callerContexts intentionally is not merged into g.AvailableContexts:
			// it lists context parameters available at the call site in the
			// caller's own function, not parameters the cross-package callee
			// actually receives. The callee's signature is not inspected here,
			// so asserting a caller-side context as an "available cancellation
			// source" for a target whose parameters are unknown would be
			// ungrounded and could suggest a context the target never receives.
			g.Evidence = append(g.Evidence, model.Evidence{Kind: "cross-package-fact", Message: "versioned lifecycle fact imported for " + obj.FullName()})
			return g
		}
	}
	return model.Goroutine{Span: siteSpan, Kind: kind, Evidence: []model.Evidence{{Kind: "unsupported", Message: "goroutine target body is not locally available and no compatible fact was found"}}}
}

func (b *builder) functionSummary(obj *types.Func, decl *ast.FuncDecl) model.Goroutine {
	if summary, ok := b.summaries[obj]; ok {
		return cloneGoroutine(summary)
	}
	contexts := map[types.Object]string{}
	b.collectContextParams(decl.Type, contexts)
	summary := b.analyzeLifecycleBody(decl.Body, contexts, "function-body", b.span(decl.Body))
	b.summaries[obj] = cloneGoroutine(summary)
	return summary
}

func (b *builder) newLifecycleSummary(body *ast.BlockStmt, contexts map[types.Object]string, kind string, span model.Span, capture bool) model.Goroutine {
	g := model.Goroutine{Span: span, Kind: kind}
	if body == nil {
		g.Evidence = append(g.Evidence, model.Evidence{Kind: "unsupported", Message: "goroutine target body is not locally available"})
		return g
	}
	var used map[types.Object]struct{}
	if capture {
		used = objectsUsed(body, b.in.Info, true)
	}
	for obj, name := range contexts {
		if capture && hasObject(used, obj) {
			g.CapturedNames = append(g.CapturedNames, name)
		}
		g.AvailableContexts = append(g.AvailableContexts, name)
	}
	sort.Strings(g.CapturedNames)
	sort.Strings(g.AvailableContexts)
	return g
}

func (b *builder) analyzeLifecycleBody(body *ast.BlockStmt, contexts map[types.Object]string, kind string, span model.Span) model.Goroutine {
	g := b.newLifecycleSummary(body, contexts, kind, span, true)
	if body == nil {
		return g
	}
	g.CFG, _ = flowgraph.Build(kind, b.in.Fset, body, b.in.Info, b.trustedTerminator(contexts))
	labels := labeledLoops(body)
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		b.observeLifecycleNode(n, contexts, labels, &g)
		return true
	})
	return g
}

// labeledLoops maps each *ast.ForStmt in body that carries a Go label
// (e.g. "Loop: for { ... }") to that label's name, so a labeled break found
// deeper in the tree can be matched back to the specific loop it targets.
// Nested function literals are excluded: a label inside a closure cannot
// target a loop outside it under Go's own scoping rules.
func labeledLoops(body ast.Node) map[*ast.ForStmt]string {
	labels := map[*ast.ForStmt]string{}
	if body == nil {
		return labels
	}
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if ls, ok := n.(*ast.LabeledStmt); ok {
			if forStmt, ok := ls.Stmt.(*ast.ForStmt); ok {
				labels[forStmt] = ls.Label.Name
			}
		}
		return true
	})
	return labels
}

// loopExitEvidence walks a single unconditional for-loop's own body for a
// recognized way out of it, scoped strictly to that loop. This replaces
// treating any break, return, channel range, or stop call anywhere in the
// enclosing goroutine body as evidence for every loop in it, regardless of
// whether the two are related.
//
// break is scoped to Go's own static target rules: an unlabeled break
// found inside a nested loop, switch, or select targets that construct,
// not this loop, so it is excluded unless it carries a label matching this
// loop's own label. return always exits the whole function regardless of
// nesting, so it counts wherever it is lexically found within this loop's
// body. A stop-wrapper call or a context passed to a called operation is
// treated the same way return is: an ordinary statement, not subject to
// break-target scoping, so any nesting depth within the loop counts.
//
// This is lexical containment plus Go's static break-target rules, not
// reachability analysis: it does not prove the identified path is
// reachable from every entry into the loop. See docs/limitations.md.
// trustedTerminator returns a predicate recognizing a call as a "trusted
// terminator" for CFG construction: a configured stop-wrapper call, or a
// call receiving one of contexts' tracked objects as an argument (context
// delegation). This is exactly the call-recognition loopExitEvidence
// already applies when collecting loop-scoped evidence, extracted into a
// form internal/cfg can consult without needing to know about config or
// tracked contexts itself -- internal/cfg only ever sees this predicate,
// never the config or contexts map it was built from.
func (b *builder) trustedTerminator(contexts map[types.Object]string) func(*ast.CallExpr) bool {
	return func(call *ast.CallExpr) bool {
		if b.hasWrapper(b.stopWrappers, b.callName(call)) {
			return true
		}
		args := objectsUsedInExpressions(call.Args, b.in.Info, true)
		for obj := range contexts {
			if hasObject(args, obj) {
				return true
			}
		}
		return false
	}
}

func (b *builder) loopExitEvidence(loop *ast.ForStmt, label string, contexts map[types.Object]string) (hasReturn, contextStop, channelStop, explicitStop bool, evidence []model.Evidence) {
	if loop.Body == nil {
		return
	}
	breakDepth := 0
	var breakable []bool
	ast.Inspect(loop.Body, func(n ast.Node) bool {
		if n == nil {
			if last := len(breakable) - 1; last >= 0 {
				if breakable[last] {
					breakDepth--
				}
				breakable = breakable[:last]
			}
			return true
		}
		switch x := n.(type) {
		case *ast.FuncLit:
			breakable = append(breakable, false)
			return false
		case *ast.SelectStmt:
			breakable = append(breakable, true)
			breakDepth++
			var sub model.Goroutine
			b.inspectSelect(x, contexts, &sub)
			if sub.ContextStop {
				contextStop = true
			}
			if sub.ChannelStop {
				channelStop = true
			}
			evidence = append(evidence, sub.Evidence...)
			return true
		case *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt:
			breakable = append(breakable, true)
			breakDepth++
			return true
		case *ast.ReturnStmt:
			hasReturn = true
			evidence = append(evidence, model.Evidence{Kind: "loop-exit", Message: "a return provides a loop exit", Span: ptrSpan(b.span(x))})
			breakable = append(breakable, false)
			return true
		case *ast.BranchStmt:
			if x.Tok == token.BREAK {
				targetsThisLoop := breakDepth == 0
				if x.Label != nil {
					targetsThisLoop = label != "" && x.Label.Name == label
				}
				if targetsThisLoop {
					hasReturn = true
					evidence = append(evidence, model.Evidence{Kind: "loop-exit", Message: "an explicit break provides a possible loop exit", Span: ptrSpan(b.span(x))})
				}
			}
			breakable = append(breakable, false)
			return true
		case *ast.CallExpr:
			callName := b.callName(x)
			if b.hasWrapper(b.stopWrappers, callName) {
				explicitStop = true
				evidence = append(evidence, model.Evidence{Kind: "configured-stop", Message: "configured stop operation: " + callName, Span: ptrSpan(b.span(x))})
			}
			args := objectsUsedInExpressions(x.Args, b.in.Info, true)
			for obj := range contexts {
				if hasObject(args, obj) {
					contextStop = true
					evidence = append(evidence, model.Evidence{Kind: "context-delegation", Message: "context is delegated to a called operation", Span: ptrSpan(b.span(x))})
					break
				}
			}
			breakable = append(breakable, false)
			return true
		default:
			breakable = append(breakable, false)
			return true
		}
	})
	return
}

func (b *builder) observeLifecycleNode(n ast.Node, contexts map[types.Object]string, labels map[*ast.ForStmt]string, g *model.Goroutine) {
	switch x := n.(type) {
	case *ast.ForStmt:
		if x.Cond == nil {
			g.InfiniteLoop = true
			g.Evidence = append(g.Evidence, model.Evidence{Kind: "infinite-loop", Message: "unconditional for loop", Span: ptrSpan(b.span(x))})
			hasReturn, contextStop, channelStop, explicitStop, loopEvidence := b.loopExitEvidence(x, labels[x], contexts)
			g.HasReturn = g.HasReturn || hasReturn
			g.ContextStop = g.ContextStop || contextStop
			g.ChannelStop = g.ChannelStop || channelStop
			g.ExplicitStop = g.ExplicitStop || explicitStop
			g.Evidence = append(g.Evidence, loopEvidence...)
		}
	}
}

func (b *builder) inspectSelect(sel *ast.SelectStmt, contexts map[types.Object]string, g *model.Goroutine) {
	for _, stmt := range sel.Body.List {
		clause, ok := stmt.(*ast.CommClause)
		if !ok || clause.Comm == nil || !statementsContainReturn(clause.Body) {
			continue
		}
		expr := receivedExpr(clause.Comm)
		if expr == nil {
			continue
		}
		if call, ok := expr.(*ast.CallExpr); ok && selectorMethod(call.Fun) == "Done" {
			if recv := selectorReceiverObject(call.Fun, b.in.Info); recv != nil {
				if _, ok := contexts[recv]; ok || isContextType(recv.Type(), b.contextInterface) {
					g.ContextStop = true
					g.Evidence = append(g.Evidence, model.Evidence{Kind: "context-select", Message: "select case returns after context cancellation", Span: ptrSpan(b.span(clause))})
					continue
				}
			}
		}
		if t := b.in.Info.TypeOf(expr); t != nil {
			if _, ok := t.Underlying().(*types.Chan); ok {
				g.ChannelStop = true
				g.Evidence = append(g.Evidence, model.Evidence{Kind: "channel-select", Message: "select case returns after a channel signal", Span: ptrSpan(b.span(clause))})
			}
		}
	}
}

func (b *builder) isConfigured(call *ast.CallExpr, set map[string]struct{}) bool {
	return b.hasWrapper(set, b.callName(call))
}

func (b *builder) hasWrapper(set map[string]struct{}, name string) bool {
	_, ok := set[name]
	return ok
}

func (b *builder) isContextFactory(name string) bool {
	switch name {
	case "context.WithCancel", "context.WithCancelCause", "context.WithDeadline", "context.WithDeadlineCause", "context.WithTimeout", "context.WithTimeoutCause":
		return true
	}
	return b.hasWrapper(b.contextFactories, name)
}

func (b *builder) callName(call *ast.CallExpr) string {
	if call == nil {
		return ""
	}
	obj := calledObject(call.Fun, b.in.Info)
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return ""
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		recv := deref(sig.Recv().Type())
		if named, ok := recv.(*types.Named); ok && named.Obj().Pkg() != nil {
			return named.Obj().Pkg().Path() + "." + named.Obj().Name() + "." + fn.Name()
		}
	}
	return fn.Pkg().Path() + "." + fn.Name()
}

func (b *builder) span(n ast.Node) model.Span {
	if n == nil {
		return model.Span{}
	}
	return spanPositions(b.in.Fset, n.Pos(), n.End())
}

func spanPositions(fset *token.FileSet, start, end token.Pos) model.Span {
	sp := fset.PositionFor(start, true)
	ep := fset.PositionFor(end, true)
	s := model.Span{File: filepath.Clean(sp.Filename), StartLine: sp.Line, StartColumn: sp.Column, EndLine: ep.Line, EndColumn: ep.Column}
	if file := fset.File(start); file != nil {
		s.StartOffset = file.Offset(start)
	}
	if file := fset.File(end); file != nil {
		s.EndOffset = file.Offset(end)
	}
	return s
}

func zeroWidthAtEnd(s model.Span) model.Span {
	s.StartLine, s.StartColumn, s.StartOffset = s.EndLine, s.EndColumn, s.EndOffset
	return s
}

func ptrSpan(s model.Span) *model.Span { return &s }

func isNamedResult(ft *ast.FuncType, obj types.Object, info *types.Info) bool {
	if ft == nil || ft.Results == nil || obj == nil {
		return false
	}
	for _, field := range ft.Results.List {
		for _, id := range field.Names {
			if info.Defs[id] == obj {
				return true
			}
		}
	}
	return false
}

func findContextInterface(pkg *types.Package) *types.Interface {
	seen := map[*types.Package]bool{}
	var visit func(*types.Package) *types.Interface
	visit = func(p *types.Package) *types.Interface {
		if p == nil || seen[p] {
			return nil
		}
		seen[p] = true
		if p.Path() == "context" {
			if obj := p.Scope().Lookup("Context"); obj != nil {
				if named, ok := obj.Type().(*types.Named); ok {
					if iface, ok := named.Underlying().(*types.Interface); ok {
						return iface.Complete()
					}
				}
			}
		}
		for _, imported := range p.Imports() {
			if iface := visit(imported); iface != nil {
				return iface
			}
		}
		return nil
	}
	return visit(pkg)
}

func isContextType(t types.Type, contextInterface *types.Interface) bool {
	if t == nil {
		return false
	}
	base := deref(t)
	if named, ok := base.(*types.Named); ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "context" && named.Obj().Name() == "Context" {
		return true
	}
	if contextInterface == nil {
		return false
	}
	return types.Implements(t, contextInterface) || (base != t && types.Implements(base, contextInterface))
}

func groupKind(t types.Type) string {
	t = deref(t)
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return ""
	}
	switch named.Obj().Pkg().Path() + "." + named.Obj().Name() {
	case "sync.WaitGroup":
		return "waitgroup"
	case "golang.org/x/sync/errgroup.Group":
		return "errgroup"
	default:
		return ""
	}
}

func deref(t types.Type) types.Type {
	if p, ok := t.(*types.Pointer); ok {
		return p.Elem()
	}
	return t
}

func calledObject(fun ast.Expr, info *types.Info) types.Object {
	switch x := fun.(type) {
	case *ast.Ident:
		return info.ObjectOf(x)
	case *ast.SelectorExpr:
		if sel := info.Selections[x]; sel != nil {
			return sel.Obj()
		}
		return info.ObjectOf(x.Sel)
	case *ast.IndexExpr:
		return calledObject(x.X, info)
	case *ast.IndexListExpr:
		return calledObject(x.X, info)
	case *ast.ParenExpr:
		return calledObject(x.X, info)
	default:
		return nil
	}
}

func selectorReceiverObject(fun ast.Expr, info *types.Info) types.Object {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	return identObject(sel.X, info)
}

func selectorMethod(fun ast.Expr) string {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	return sel.Sel.Name
}

func identObject(expr ast.Expr, info *types.Info) types.Object {
	switch x := expr.(type) {
	case *ast.Ident:
		return info.ObjectOf(x)
	case *ast.ParenExpr:
		return identObject(x.X, info)
	case *ast.UnaryExpr:
		return identObject(x.X, info)
	default:
		return nil
	}
}

func objectsUsedInExpressions(exprs []ast.Expr, info *types.Info, skipFuncLits bool) map[types.Object]struct{} {
	out := map[types.Object]struct{}{}
	for _, expr := range exprs {
		for obj := range objectsUsed(expr, info, skipFuncLits) {
			out[obj] = struct{}{}
		}
	}
	return out
}

func objectsUsed(n ast.Node, info *types.Info, skipFuncLits bool) map[types.Object]struct{} {
	out := map[types.Object]struct{}{}
	ast.Inspect(n, func(child ast.Node) bool {
		if child == nil {
			return true
		}
		if skipFuncLits {
			if _, ok := child.(*ast.FuncLit); ok {
				return false
			}
		}
		if id, ok := child.(*ast.Ident); ok {
			if obj := info.ObjectOf(id); obj != nil {
				out[obj] = struct{}{}
			}
		}
		return true
	})
	return out
}

func hasObject(set map[types.Object]struct{}, obj types.Object) bool {
	if obj == nil {
		return false
	}
	_, ok := set[obj]
	return ok
}

// firstStartTarget finds the first argument to a configured start-wrapper
// call that can represent a goroutine's entry point: either an inline
// function literal, or a reference whose static type is a function type
// (e.g. Launch(myWorker) where myWorker is a top-level declared function).
// A function literal is preferred over a function-typed reference when both
// are present, matching the argument priority the closure-only version of
// this check used. Whether a function-typed reference can actually be
// resolved to a specific declaration (as opposed to, say, a local variable
// or struct field of function type, which is not resolved) is decided by
// buildGoroutine itself, via the same calledObject resolution it already
// applies to `go` statements — this keeps both call sites conservative in
// exactly the same way rather than duplicating that decision here.
func firstStartTarget(args []ast.Expr, info *types.Info) ast.Expr {
	for _, arg := range args {
		if _, ok := arg.(*ast.FuncLit); ok {
			return arg
		}
	}
	for _, arg := range args {
		t := info.TypeOf(arg)
		if t == nil {
			continue
		}
		if _, ok := t.Underlying().(*types.Signature); ok {
			return arg
		}
	}
	return nil
}

func statementsContainReturn(stmts []ast.Stmt) bool {
	for _, stmt := range stmts {
		found := false
		ast.Inspect(stmt, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			if _, ok := n.(*ast.ReturnStmt); ok {
				found = true
				return false
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

func receivedExpr(stmt ast.Stmt) ast.Expr {
	switch x := stmt.(type) {
	case *ast.ExprStmt:
		if u, ok := x.X.(*ast.UnaryExpr); ok && u.Op == token.ARROW {
			return u.X
		}
	case *ast.AssignStmt:
		for _, rhs := range x.Rhs {
			if u, ok := rhs.(*ast.UnaryExpr); ok && u.Op == token.ARROW {
				return u.X
			}
		}
	}
	return nil
}

func identifierNames(n ast.Node) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(n, func(child ast.Node) bool {
		if id, ok := child.(*ast.Ident); ok {
			out[id.Name] = true
		}
		return true
	})
	return out
}

func uniqueName(base string, used map[string]bool) string {
	if !used[base] {
		return base
	}
	for i := 2; ; i++ {
		name := fmt.Sprintf("%s%d", base, i)
		if !used[name] {
			return name
		}
	}
}

func stringSet(list []string) map[string]struct{} {
	out := make(map[string]struct{}, len(list))
	for _, item := range list {
		out[item] = struct{}{}
	}
	return out
}

func cloneGoroutine(g model.Goroutine) model.Goroutine {
	g.AvailableContexts = append([]string(nil), g.AvailableContexts...)
	g.CapturedNames = append([]string(nil), g.CapturedNames...)
	g.Evidence = append([]model.Evidence(nil), g.Evidence...)
	return g
}

func appendUnique(list []string, value string) []string {
	for _, item := range list {
		if item == value {
			return list
		}
	}
	return append(list, value)
}
