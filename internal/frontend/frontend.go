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
	// unanalyzable. See argumentConsumed for how a lookup here degrades
	// safely (falls back to the prior unconditional-escape behavior)
	// whenever this pre-pass didn't reach a particular callee at all --
	// max_functions truncation being the main reason it wouldn't.
	for _, source := range sources[:limit] {
		b.computeParameterConsumption(source.decl)
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
}

type groupState struct {
	group model.JoinGroup
	obj   types.Object
	// waitCallSites is the same kind of AST-node record as
	// cancelState.callSites above, for the same reason: finding each
	// Wait() call's CFG block by identity, not by span.
	waitCallSites []*ast.CallExpr
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
	fn.BodyLifecycle.CFG, _ = flowgraph.Build(name, b.in.Fset, fd.Body, b.in.Info, b.trustedTerminator(contexts))
	b.observeFunctionBody(fd.Body, contexts, states, groups, &fn)
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

// computeParameterConsumption populates b.paramConsumption for every
// cancel-like or group-like parameter of fd, by running the same
// Called/Escapes detection machinery used for locally-declared cancel and
// group bindings (observeFunctionBody) against fd's own body, with those
// parameters standing in for what would otherwise be locally-declared
// bindings. This is Phase 5's "direct parameter passing" support
// (docs/cfg-migration-plan.md): it lets argumentConsumed later tell
// whether a cancel/group value passed as an argument is actually consumed
// by the callee, rather than unconditionally trusting that passing it
// anywhere discharges the caller's own obligation.
//
// The resulting model.Function is discarded: this call exists only for
// its side effect on b.paramConsumption, not to produce a second copy of
// fd's own diagnostics (buildFunction does that, separately, for fd's own
// locally-declared bindings).
func (b *builder) computeParameterConsumption(fd *ast.FuncDecl) {
	if fd.Type.Params == nil {
		return
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
		return
	}
	contexts := map[types.Object]string{}
	b.collectContextParams(fd.Type, contexts)
	var scratch model.Function
	b.observeFunctionBody(fd.Body, contexts, paramCancels, paramGroups, &scratch)
	for _, c := range paramCancels {
		b.paramConsumption[c.cancelObj] = c.binding.Called || c.binding.Escapes
	}
	for _, g := range paramGroups {
		b.paramConsumption[g.obj] = g.group.Joined || g.group.Escapes
	}
}

// argumentConsumed reports whether obj, passed directly as call's argument
// at some position, is consumed by the callee's own body: a pure
// model.Function lookup into b.paramConsumption, populated by
// computeParameterConsumption's pre-pass. This never triggers a fresh
// analysis (unlike, say, functionSummary's on-demand fallback), which is
// what keeps it safe from recursion regardless of how functions call each
// other: a lookup miss just means verified is false, not that anything
// gets computed here.
//
// verified is false whenever the check can't be made with confidence: the
// callee isn't a statically resolvable same-package function, the call
// uses `...` spread (whose positional argument-to-parameter mapping this
// does not attempt), obj isn't found as a direct argument expression (only
// the direct case is handled, not a nested sub-expression), or the
// pre-pass never reached the callee (e.g. max_functions truncation).
// Callers should fall back to the prior unconditional "assume the
// obligation was transferred" behavior when verified is false, never
// assume a leak from an inability to check.
func (b *builder) argumentConsumed(call *ast.CallExpr, obj types.Object) (consumed, verified bool) {
	if call.Ellipsis.IsValid() {
		return false, false
	}
	funcObj, ok := calledObject(call.Fun, b.in.Info).(*types.Func)
	if !ok {
		return false, false
	}
	decl := b.funcs[funcObj]
	if decl == nil {
		return false, false
	}
	index := -1
	for i, arg := range call.Args {
		if identObject(arg, b.in.Info) == obj {
			index = i
			break
		}
	}
	if index == -1 {
		return false, false
	}
	paramObj := paramObjectAtIndex(b.in.Info, decl, index)
	if paramObj == nil {
		return false, false
	}
	consumed, ok = b.paramConsumption[paramObj]
	return consumed, ok
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
		if c.cancelObj != nil && hasObject(argObjects, c.cancelObj) {
			if consumed, verified := b.argumentConsumed(call, c.cancelObj); verified {
				if consumed {
					c.binding.Escapes = true
					c.binding.Evidence = append(c.binding.Evidence, model.Evidence{Kind: "parameter-consumed", Message: "passed as an argument; the callee's own body consumes it", Span: ptrSpan(b.span(call))})
				} else {
					c.binding.Evidence = append(c.binding.Evidence, model.Evidence{Kind: "parameter-not-consumed", Message: "passed as an argument, but the callee's own body never calls or further transfers it", Span: ptrSpan(b.span(call))})
				}
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
			// group passed as an argument rather than joined directly.
			if consumed, verified := b.argumentConsumed(call, g.obj); verified {
				if consumed {
					g.group.Escapes = true
					g.group.Evidence = append(g.group.Evidence, model.Evidence{Kind: "parameter-consumed", Message: "passed as an argument; the callee's own body joins or further transfers it", Span: ptrSpan(b.span(call))})
				} else {
					g.group.Evidence = append(g.group.Evidence, model.Evidence{Kind: "parameter-not-consumed", Message: "passed as an argument, but the callee's own body never joins or further transfers it", Span: ptrSpan(b.span(call))})
				}
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
// same-package function that receives obj as a direct argument at some
// position, and whose own body calls Done() on that corresponding
// parameter -- the named-function counterpart to bodyCallsMethodOn's
// closure-capture check above, for the equally common `wg.Add(1); go
// worker(&wg)` idiom, where worker's whole job is to call Done() on
// whatever it's given. This deliberately does not reuse
// b.paramConsumption (Phase 5's own fixed point for cancel/group
// parameters): that answers a different question -- does the parameter
// get Wait()ed or further transferred -- appropriate for verifying an
// ownership handoff, not for a worker whose job is specifically to
// decrement the counter its caller already incremented. This is a one-hop
// existence check, not a fixed point: if worker itself delegates to a
// further helper that calls Done(), that is not attempted here.
func (b *builder) calleeDoneParamMatches(call *ast.CallExpr, obj types.Object) bool {
	funcObj, ok := calledObject(call.Fun, b.in.Info).(*types.Func)
	if !ok {
		return false
	}
	decl := b.funcs[funcObj]
	if decl == nil {
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
	paramObj := paramObjectAtIndex(b.in.Info, decl, index)
	if paramObj == nil {
		return false
	}
	return bodyCallsMethodOn(decl.Body, paramObj, "Done", b.in.Info)
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
func (b *builder) computeGroupOrdering(groups []*groupState, cancels []*cancelState, body *ast.BlockStmt, g *model.CFG, callSites map[*ast.CallExpr]flowgraph.CallSite) {
	if g == nil || len(groups) == 0 {
		return
	}
	var stopSignals []*ast.CallExpr
	for _, c := range cancels {
		if c.binding.Called && c.binding.UsedByChild {
			stopSignals = append(stopSignals, c.callSites...)
		}
	}
	stopSignals = append(stopSignals, b.collectStopWrapperCalls(body)...)
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
