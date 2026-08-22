package frontend

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
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

	contextInterface *types.Interface
	contextFactories map[string]struct{}
	startWrappers    map[string]struct{}
	joinWrappers     map[string]struct{}
	stopWrappers     map[string]struct{}
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
		in:               in,
		cfg:              cfg,
		funcs:            map[*types.Func]*ast.FuncDecl{},
		analyzed:         map[*types.Func]bool{},
		summaries:        map[*types.Func]model.Goroutine{},
		paramConsumption: map[types.Object]bool{},
		contextInterface: findContextInterface(in.Pkg),
		contextFactories: stringSet(cfg.ContextWrappers),
		startWrappers:    stringSet(cfg.StartWrappers),
		joinWrappers:     stringSet(cfg.JoinWrappers),
		stopWrappers:     stringSet(cfg.StopWrappers),
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
	for {
		changed := false
		for _, source := range sources[:limit] {
			if b.computeParameterConsumption(source.decl) {
				changed = true
			}
		}
		if !changed {
			break
		}
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
}

type groupState struct {
	group model.JoinGroup
	obj   types.Object
}

func (b *builder) buildFunction(source funcSource) model.Function {
	fd := source.decl
	name := fd.Name.Name
	if source.obj != nil {
		name = source.obj.FullName()
	}
	fn := model.Function{Name: name, Span: b.span(fd)}
	contexts := map[types.Object]string{}
	b.collectContextParams(fd.Type, contexts)
	states, groups := b.collectBindings(fd, contexts)

	fn.BodyLifecycle = b.newLifecycleSummary(fd.Body, contexts, "function-body", b.span(fd.Body), false)
	fn.BodyLifecycle.CFG = flowgraph.Build(name, b.in.Fset, fd.Body, b.in.Info, b.trustedTerminator(contexts))
	b.observeFunctionBody(fd.Body, contexts, states, groups, &fn)
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

func (b *builder) observeFunctionBody(body *ast.BlockStmt, contexts map[types.Object]string, cancels []*cancelState, groups []*groupState, fn *model.Function) {
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
			b.observeReturn(x, cancels, groups)
		case *ast.AssignStmt:
			b.observeEscapeAssignment(x, cancels, groups)
		case *ast.CompositeLit:
			b.observeContainerEscape(x, cancels, groups)
		case *ast.ValueSpec:
			b.observeContainerEscape(x, cancels, groups)
		case *ast.GoStmt:
			g := b.buildGoroutine(x, x.Call, contexts, "go")
			fn.Goroutines = append(fn.Goroutines, g)
			b.markChildUses(x.Call, cancels)
		}
		return true
	})
}

// computeParameterConsumption (re)computes, for every cancel-like or
// group-like parameter of fd, whether fd's own body consumes it, by
// running the same Called/Escapes detection machinery used for
// locally-declared cancel and group bindings (observeFunctionBody)
// against fd's own body, with those parameters standing in for what would
// otherwise be locally-declared bindings. This is Phase 5's "direct
// parameter passing" support (docs/cfg-migration-plan.md): it lets
// argumentConsumed later tell whether a cancel/group value passed as an
// argument is actually consumed by the callee, rather than unconditionally
// trusting that passing it anywhere discharges the caller's own
// obligation.
//
// Build calls this once per function per sweep, as part of a fixed-point
// loop, not once ever: fd's own body may itself pass a parameter on to
// another function whose result isn't known yet on an early sweep, in
// which case that call contributes nothing this time (see inParamPrepass
// and argumentConsumed's pending result) and fd's recorded value may still
// rise on a later sweep once that dependency resolves. Recording is
// monotone -- merged with whatever was already there, via OR, never
// overwritten with a lower value -- both because that is the only
// direction new information moves in this analysis (something already
// shown to be consumed stays consumed) and because it is what makes the
// fixed point well-defined regardless of how many times or in what order
// this runs. reportChanged is true iff this call caused any entry to
// change (including a first-time recording of "false"; recording that a
// callee's result is known, even if the answer is "not consumed", is
// itself new information a caller depends on -- see argumentConsumed's
// pending case), which is what tells Build's loop whether another sweep
// is needed.
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
	if len(paramCancels) == 0 && len(paramGroups) == 0 {
		return false
	}
	contexts := map[types.Object]string{}
	b.collectContextParams(fd.Type, contexts)
	var scratch model.Function
	b.inParamPrepass = true
	b.observeFunctionBody(fd.Body, contexts, paramCancels, paramGroups, &scratch)
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
// at some position, is consumed by the callee's own body: a pure
// model.Function lookup into b.paramConsumption, populated by
// computeParameterConsumption's fixed-point loop. This never triggers a
// fresh analysis (unlike, say, functionSummary's on-demand fallback),
// which is what keeps it safe from recursion regardless of how functions
// call each other: a lookup miss just means verified is false, not that
// anything gets computed here.
//
// verified is false for two quite different reasons, which pending tells
// apart:
//
//   - pending == false: the check can never be made with confidence for
//     this call, no matter how many times it's retried -- the callee isn't
//     a statically resolvable same-package function, the call uses `...`
//     spread (whose positional argument-to-parameter mapping this does not
//     attempt), or obj isn't found as a direct argument expression (only
//     the direct case is handled, not a nested sub-expression). Callers
//     should fall back to the prior unconditional "assume the obligation
//     was transferred" behavior here, never assume a leak from an
//     inability to check.
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
//     as the never-resolvable case.
func (b *builder) argumentConsumed(call *ast.CallExpr, obj types.Object) (consumed, verified, pending bool) {
	if call.Ellipsis.IsValid() {
		return false, false, false
	}
	funcObj, ok := calledObject(call.Fun, b.in.Info).(*types.Func)
	if !ok {
		return false, false, false
	}
	decl := b.funcs[funcObj]
	if decl == nil {
		return false, false, false
	}
	index := -1
	for i, arg := range call.Args {
		if identObject(arg, b.in.Info) == obj {
			index = i
			break
		}
	}
	if index == -1 {
		return false, false, false
	}
	paramObj := paramObjectAtIndex(b.in.Info, decl, index)
	if paramObj == nil {
		return false, false, false
	}
	consumed, ok = b.paramConsumption[paramObj]
	return consumed, ok, !ok
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

func (b *builder) observeCall(call *ast.CallExpr, cancels []*cancelState, groups []*groupState) {
	name := b.callName(call)
	funObj := calledObject(call.Fun, b.in.Info)
	argObjects := objectsUsedInExpressions(call.Args, b.in.Info, true)
	for _, c := range cancels {
		if c.cancelObj != nil && funObj == c.cancelObj {
			c.binding.Called = true
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
		if c.cancelObj != nil && hasObject(argObjects, c.cancelObj) {
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
				g.group.Evidence = append(g.group.Evidence, model.Evidence{Kind: "join", Message: "group is joined here", Span: ptrSpan(b.span(call))})
			}
		}
		usesGroup := hasObject(argObjects, g.obj)
		if b.hasWrapper(b.joinWrappers, name) && usesGroup {
			g.group.Joined = true
		}
		if receiver != g.obj && usesGroup && !b.hasWrapper(b.joinWrappers, name) {
			// Same Phase 5 verification as cancels above, applied to a
			// group passed as an argument rather than joined directly,
			// including the same mid-fixed-point "pending" carve-out.
			if consumed, verified, pending := b.argumentConsumed(call, g.obj); verified {
				if consumed {
					g.group.Escapes = true
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

func (b *builder) observeReturn(ret *ast.ReturnStmt, cancels []*cancelState, groups []*groupState) {
	used := objectsUsedInExpressions(ret.Results, b.in.Info, true)
	for _, c := range cancels {
		if c.cancelObj != nil && hasObject(used, c.cancelObj) {
			c.binding.Escapes = true
		}
	}
	for _, g := range groups {
		if g.obj != nil && hasObject(used, g.obj) {
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
		for _, c := range cancels {
			if obj == c.cancelObj {
				c.binding.Escapes = true
			}
		}
		for _, g := range groups {
			if obj == g.obj {
				g.group.Escapes = true
			}
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

func (b *builder) observeContainerEscape(n ast.Node, cancels []*cancelState, groups []*groupState) {
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
			if c.cancelObj != nil && hasObject(used, c.cancelObj) {
				c.binding.Escapes = true
				c.binding.Evidence = append(c.binding.Evidence, model.Evidence{Kind: "ownership-transfer", Message: "cancellation function is stored in another value", Span: ptrSpan(b.span(node))})
			}
		}
		for _, g := range groups {
			if g.obj != nil && hasObject(used, g.obj) {
				g.group.Escapes = true
			}
		}
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
	g.CFG = flowgraph.Build(kind, b.in.Fset, body, b.in.Info, b.trustedTerminator(contexts))
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
