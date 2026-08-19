// Package cfg builds a parser-independent control-flow graph (model.CFG)
// from a typed Go AST function or function-literal body.
//
// This is a structural pass only: it makes control flow explicit (blocks,
// branches, loop back-edges, break/continue/goto targets, switch/select
// case edges, return/panic edges) so later analysis can reason about
// reachability and cycles directly on the graph, instead of aggregating
// "does this body contain a return/break/select-with-ctx.Done() anywhere"
// booleans the way internal/frontend's lifecycle summaries do today.
//
// Building a CFG does not by itself change any diagnostic: nothing in the
// existing rule engine consumes this package yet. It exists to be dumped,
// inspected, and tested against real code first, so that a subsequent pass
// can migrate specific rules (starting with LL1002) onto it deliberately,
// with the graph's correctness already established independently.
package cfg

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"github.com/gfedyukovich/lifeline/internal/model"
)

// Build constructs a CFG for a single function or function-literal body.
// name is a display name only (e.g. "pkg.Func" or "pkg.Func.func1"); it does
// not affect graph structure. fset and info are required for spans and for
// resolving call callees and range-over-channel detection.
//
// isTrustedTerminator, if non-nil, is consulted for every call expression
// encountered as a statement (or as the right-hand side of an assignment or
// send): if it reports true, that call is treated as terminating control
// flow, with an edge straight to Exit (EdgeTrustedStop), the same way a
// panic is. This exists for signals that are not visible in pure control
// flow at all -- a configured stop-wrapper call, or a tracked context
// passed to a called operation -- where the caller (internal/frontend,
// which knows the relevant config and which objects are tracked contexts)
// has already decided the call should be trusted to terminate. internal/cfg
// itself has no notion of config or tracked contexts; it only acts on what
// this predicate tells it. Pass nil for a purely structural CFG, e.g. for
// -dump cfg, which has no such trust decision to apply.
//
// Nested function literals are not descended into, matching the rest of
// Lifeline's architecture: each function literal has its own independent
// CFG, built by a separate Build call when the caller needs one (e.g. for a
// goroutine body). A `go`/`defer` statement whose argument is a FuncLit is
// still recorded as a single instruction in the enclosing CFG; its body is
// not inlined.
func Build(name string, fset *token.FileSet, body *ast.BlockStmt, info *types.Info, isTrustedTerminator func(*ast.CallExpr) bool) *model.CFG {
	b := &builder{fset: fset, info: info, labels: map[string]model.BlockID{}, isTrustedTerminator: isTrustedTerminator}
	entry := b.newBlock("entry")
	exit := b.newBlock("exit")
	b.exit = exit
	b.current = entry
	if body != nil {
		b.preAllocateLabels(body)
		b.stmtList(body.List)
	}
	if b.current != invalidBlock {
		b.addEdge(b.current, exit, model.EdgeNormal, "", b.spanOf(body))
	}
	return &model.CFG{Function: name, Entry: entry, Exit: exit, Blocks: b.blocks}
}

const invalidBlock = model.BlockID(-1)

// frame is one entry of the break/continue resolution stack: a loop gets a
// continue target, a bare switch/select does not (continue always skips
// past switch/select to the nearest enclosing loop, per Go's own scoping).
type frame struct {
	label      string
	isLoop     bool
	breakTo    model.BlockID
	continueTo model.BlockID // only meaningful when isLoop
}

type builder struct {
	fset                *token.FileSet
	info                *types.Info
	blocks              []model.BasicBlock
	current             model.BlockID
	frames              []frame
	labels              map[string]model.BlockID // label name -> pre-allocated block, for break/continue/goto targets
	exit                model.BlockID
	isTrustedTerminator func(*ast.CallExpr) bool
}

func (b *builder) newBlock(kind string) model.BlockID {
	id := model.BlockID(len(b.blocks))
	b.blocks = append(b.blocks, model.BasicBlock{ID: id, Kind: kind})
	return id
}

func (b *builder) addEdge(from, to model.BlockID, kind model.EdgeKind, label string, span model.Span) {
	if from == invalidBlock || to == invalidBlock {
		return
	}
	b.blocks[from].Successors = append(b.blocks[from].Successors, model.Edge{From: from, To: to, Kind: kind, Label: label, Span: span})
	b.blocks[to].Predecessors = append(b.blocks[to].Predecessors, from)
}

// ensureCurrent guarantees b.current is a valid block to attach the next
// statement to. It is invalid immediately after a terminating statement
// (return, break, continue, goto, panic, or an exhausted branch); anything
// that follows in the same statement list is unreachable but may still be
// syntactically present (Go does not reject dead code the way some
// compilers do), so it gets its own block with zero predecessors rather
// than being silently dropped.
func (b *builder) ensureCurrent() model.BlockID {
	if b.current == invalidBlock {
		b.current = b.newBlock("unreachable")
	}
	return b.current
}

func (b *builder) emit(op string, n ast.Node, callee string, defines, uses []string) {
	cur := b.ensureCurrent()
	instr := model.Instruction{Index: len(b.blocks[cur].Instructions), Op: op, Span: b.spanOf(n), Callee: callee, Defines: defines, Uses: uses}
	b.blocks[cur].Instructions = append(b.blocks[cur].Instructions, instr)
	b.extendSpan(cur, n)
}

func (b *builder) extendSpan(id model.BlockID, n ast.Node) {
	if n == nil {
		return
	}
	s := b.spanOf(n)
	blk := &b.blocks[id]
	if blk.Span.File == "" {
		blk.Span = s
		return
	}
	if s.StartOffset < blk.Span.StartOffset || blk.Span.StartOffset == 0 && blk.Span.EndOffset == 0 {
		if blk.Span.StartOffset == 0 || s.StartOffset < blk.Span.StartOffset {
			blk.Span.File = s.File
			blk.Span.StartLine, blk.Span.StartColumn, blk.Span.StartOffset = s.StartLine, s.StartColumn, s.StartOffset
		}
	}
	if s.EndOffset > blk.Span.EndOffset {
		blk.Span.EndLine, blk.Span.EndColumn, blk.Span.EndOffset = s.EndLine, s.EndColumn, s.EndOffset
	}
}

func (b *builder) spanOf(n ast.Node) model.Span {
	if n == nil || b.fset == nil {
		return model.Span{}
	}
	start := b.fset.Position(n.Pos())
	end := b.fset.Position(n.End())
	return model.Span{
		File: start.Filename, StartLine: start.Line, StartColumn: start.Column,
		EndLine: end.Line, EndColumn: end.Column, StartOffset: start.Offset, EndOffset: end.Offset,
	}
}

// preAllocateLabels finds every labeled statement in body up front (not
// just loop/switch/select labels) so a `goto` can target a block that
// hasn't been reached by normal traversal yet (a forward goto), not only
// ones already processed.
func (b *builder) preAllocateLabels(body ast.Node) {
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if ls, ok := n.(*ast.LabeledStmt); ok {
			b.labels[ls.Label.Name] = b.newBlock("label:" + ls.Label.Name)
		}
		return true
	})
}

func (b *builder) resolveBreak(label string) (model.BlockID, bool) {
	for i := len(b.frames) - 1; i >= 0; i-- {
		f := b.frames[i]
		if label == "" || f.label == label {
			return f.breakTo, true
		}
	}
	return invalidBlock, false
}

func (b *builder) resolveContinue(label string) (model.BlockID, bool) {
	for i := len(b.frames) - 1; i >= 0; i-- {
		f := b.frames[i]
		if !f.isLoop {
			continue
		}
		if label == "" || f.label == label {
			return f.continueTo, true
		}
	}
	return invalidBlock, false
}

// stmtList processes statements in sequence, threading b.current through
// them the way control actually flows.
func (b *builder) stmtList(list []ast.Stmt) {
	for _, s := range list {
		b.stmt(s, "")
	}
}

// stmt processes a single statement. pendingLabel is the label of an
// enclosing *ast.LabeledStmt when this statement is that label's direct
// target (for(); switch; select accept a label for break/continue
// resolution); it is empty otherwise.
func (b *builder) stmt(s ast.Stmt, pendingLabel string) {
	switch x := s.(type) {
	case nil:
		return
	case *ast.BlockStmt:
		b.stmtList(x.List)
	case *ast.LabeledStmt:
		target, ok := b.labels[x.Label.Name]
		if !ok {
			target = b.newBlock("label:" + x.Label.Name)
			b.labels[x.Label.Name] = target
		}
		if b.current != invalidBlock && b.current != target {
			b.addEdge(b.current, target, model.EdgeNormal, "", b.spanOf(x))
		}
		b.current = target
		b.stmt(x.Stmt, x.Label.Name)
	case *ast.ExprStmt:
		b.simpleStmt(x)
	case *ast.AssignStmt:
		b.simpleStmt(x)
	case *ast.IncDecStmt:
		b.simpleStmt(x)
	case *ast.SendStmt:
		b.simpleStmt(x)
	case *ast.DeclStmt:
		b.emit("decl", x, "", nil, nil)
	case *ast.GoStmt:
		b.emit("go", x, calleeName(b.info, x.Call), nil, nil)
	case *ast.DeferStmt:
		b.emit("defer", x, calleeName(b.info, x.Call), nil, nil)
	case *ast.EmptyStmt:
		// no-op
	case *ast.IfStmt:
		b.ifStmt(x)
	case *ast.ForStmt:
		b.forStmt(x, pendingLabel)
	case *ast.RangeStmt:
		b.rangeStmt(x, pendingLabel)
	case *ast.SwitchStmt:
		b.switchStmt(x, pendingLabel)
	case *ast.TypeSwitchStmt:
		b.typeSwitchStmt(x, pendingLabel)
	case *ast.SelectStmt:
		b.selectStmt(x, pendingLabel)
	case *ast.ReturnStmt:
		b.emit("return", x, "", nil, nil)
		b.addEdge(b.ensureCurrent(), b.exit, model.EdgeReturn, "", b.spanOf(x))
		b.current = invalidBlock
	case *ast.BranchStmt:
		b.branchStmt(x)
	default:
		// Statement kinds not explicitly handled (e.g. type declarations
		// nested in a statement position) are recorded as opaque,
		// non-branching instructions rather than silently dropped.
		b.emit("stmt", s, "", nil, nil)
	}
}

func (b *builder) simpleStmt(s ast.Stmt) {
	if _, ok := b.findCall(s, b.isPanicCall); ok {
		b.emit("panic", s, "panic", nil, nil)
		b.addEdge(b.ensureCurrent(), b.exit, model.EdgePanic, "", b.spanOf(s))
		b.current = invalidBlock
		return
	}
	if b.isTrustedTerminator != nil {
		if call, ok := b.findCall(s, b.isTrustedTerminator); ok {
			b.emit("trusted-stop", s, calleeName(b.info, call), nil, nil)
			b.addEdge(b.ensureCurrent(), b.exit, model.EdgeTrustedStop, "", b.spanOf(s))
			b.current = invalidBlock
			return
		}
	}
	op := "stmt"
	callee := ""
	switch x := s.(type) {
	case *ast.AssignStmt:
		op = "assign"
	case *ast.ExprStmt:
		if call, ok := x.X.(*ast.CallExpr); ok {
			op = "call"
			callee = calleeName(b.info, call)
		}
	}
	b.emit(op, s, callee, nil, nil)
}

// findCall looks anywhere within statement s (not just at its top level --
// e.g. also inside a call's arguments) for a call expression matching
// predicate, without descending into a nested function literal. This
// matches the scope internal/frontend's loop-scoped evidence collection
// already uses for the same kinds of calls (stop wrappers, context
// delegation), so a CFG built with a trust predicate derived from that
// mechanism recognizes exactly the same calls it does.
func (b *builder) findCall(s ast.Stmt, predicate func(*ast.CallExpr) bool) (*ast.CallExpr, bool) {
	var found *ast.CallExpr
	ast.Inspect(s, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok && predicate(call) {
			found = call
			return false
		}
		return true
	})
	return found, found != nil
}

func (b *builder) isPanicCall(call *ast.CallExpr) bool {
	id, ok := call.Fun.(*ast.Ident)
	if !ok || id.Name != "panic" {
		return false
	}
	if b.info == nil {
		return true
	}
	obj := b.info.ObjectOf(id)
	if obj == nil {
		return true // unresolved builtin in test fixtures without full type info; stay permissive
	}
	_, isBuiltin := obj.(*types.Builtin)
	return isBuiltin
}

func (b *builder) ifStmt(x *ast.IfStmt) {
	if x.Init != nil {
		b.stmt(x.Init, "")
	}
	cond := b.ensureCurrent()
	thenBlk := b.newBlock("if-then")
	after := b.newBlock("if-after")
	condSpan := b.spanOf(x.Cond)
	b.addEdge(cond, thenBlk, model.EdgeTrue, renderExpr(x.Cond), condSpan)

	b.current = thenBlk
	b.stmt(x.Body, "")
	if b.current != invalidBlock {
		b.addEdge(b.current, after, model.EdgeNormal, "", model.Span{})
	}

	if x.Else != nil {
		elseBlk := b.newBlock("if-else")
		b.addEdge(cond, elseBlk, model.EdgeFalse, "", condSpan)
		b.current = elseBlk
		b.stmt(x.Else, "")
		if b.current != invalidBlock {
			b.addEdge(b.current, after, model.EdgeNormal, "", model.Span{})
		}
	} else {
		b.addEdge(cond, after, model.EdgeFalse, "", condSpan)
	}
	b.current = after
}

func (b *builder) forStmt(x *ast.ForStmt, label string) {
	if x.Init != nil {
		b.stmt(x.Init, "")
	}
	header := b.newBlock("loop-header")
	b.addEdge(b.ensureCurrent(), header, model.EdgeNormal, "", b.spanOf(x))

	body := b.newBlock("loop-body")
	after := b.newBlock("loop-after")

	var post model.BlockID
	continueTo := header
	if x.Post != nil {
		post = b.newBlock("loop-post")
		continueTo = post
	}

	if x.Cond != nil {
		condSpan := b.spanOf(x.Cond)
		b.addEdge(header, body, model.EdgeTrue, renderExpr(x.Cond), condSpan)
		b.addEdge(header, after, model.EdgeFalse, "", condSpan)
	} else {
		// An unconditional loop has no false-edge out of the header at all:
		// the only way to reach `after` is an explicit break (or a return).
		// This absence is itself the structural signal a later SCC-based
		// pass can use, in place of today's flat "InfiniteLoop" boolean.
		b.addEdge(header, body, model.EdgeNormal, "", b.spanOf(x))
	}

	b.frames = append(b.frames, frame{label: label, isLoop: true, breakTo: after, continueTo: continueTo})
	b.current = body
	b.stmt(x.Body, "")
	if b.current != invalidBlock {
		if x.Post != nil {
			b.addEdge(b.current, post, model.EdgeNormal, "", model.Span{})
		} else {
			b.addEdge(b.current, header, model.EdgeLoopBack, "", b.spanOf(x))
		}
	}
	if x.Post != nil {
		b.current = post
		b.stmt(x.Post, "")
		b.addEdge(b.ensureCurrent(), header, model.EdgeLoopBack, "", b.spanOf(x.Post))
	}
	b.frames = b.frames[:len(b.frames)-1]
	b.current = after
}

func (b *builder) rangeStmt(x *ast.RangeStmt, label string) {
	header := b.newBlock("range-header")
	isChan := false
	if b.info != nil {
		if t := b.info.TypeOf(x.X); t != nil {
			if _, ok := t.Underlying().(*types.Chan); ok {
				isChan = true
			}
		}
	}
	kind := "sequence exhausted"
	if isChan {
		kind = "channel closed"
	}
	b.addEdge(b.ensureCurrent(), header, model.EdgeNormal, "", b.spanOf(x))

	body := b.newBlock("range-body")
	after := b.newBlock("range-after")
	rangeSpan := b.spanOf(x.X)
	b.addEdge(header, body, model.EdgeTrue, "has next", rangeSpan)
	b.addEdge(header, after, model.EdgeFalse, kind, rangeSpan)

	b.frames = append(b.frames, frame{label: label, isLoop: true, breakTo: after, continueTo: header})
	b.current = body
	b.stmt(x.Body, "")
	if b.current != invalidBlock {
		b.addEdge(b.current, header, model.EdgeLoopBack, "", b.spanOf(x))
	}
	b.frames = b.frames[:len(b.frames)-1]
	b.current = after
}

func (b *builder) switchStmt(x *ast.SwitchStmt, label string) {
	if x.Init != nil {
		b.stmt(x.Init, "")
	}
	head := b.ensureCurrent()
	after := b.newBlock("switch-after")
	b.frames = append(b.frames, frame{label: label, isLoop: false, breakTo: after})

	hasDefault := false
	var caseBlocks []model.BlockID
	for _, c := range x.Body.List {
		cc := c.(*ast.CaseClause)
		if cc.List == nil {
			hasDefault = true
		}
		caseBlocks = append(caseBlocks, b.newBlock("case"))
	}
	if !hasDefault {
		b.addEdge(head, after, model.EdgeNormal, "no match", b.spanOf(x))
	}
	b.switchCaseBodies(head, x.Body.List, caseBlocks, after, func(cc *ast.CaseClause) []ast.Expr { return cc.List })
	b.frames = b.frames[:len(b.frames)-1]
	b.current = after
}

func (b *builder) typeSwitchStmt(x *ast.TypeSwitchStmt, label string) {
	if x.Init != nil {
		b.stmt(x.Init, "")
	}
	head := b.ensureCurrent()
	after := b.newBlock("switch-after")
	b.frames = append(b.frames, frame{label: label, isLoop: false, breakTo: after})

	hasDefault := false
	var caseBlocks []model.BlockID
	for _, c := range x.Body.List {
		cc := c.(*ast.CaseClause)
		if cc.List == nil {
			hasDefault = true
		}
		caseBlocks = append(caseBlocks, b.newBlock("case"))
	}
	if !hasDefault {
		b.addEdge(head, after, model.EdgeNormal, "no match", b.spanOf(x))
	}
	b.switchCaseBodies(head, x.Body.List, caseBlocks, after, func(cc *ast.CaseClause) []ast.Expr { return cc.List })
	b.frames = b.frames[:len(b.frames)-1]
	b.current = after
}

// switchCaseBodies wires case edges and processes each case's statement
// list, including `fallthrough` (valid only as a case's last statement,
// per Go's grammar): it jumps directly into the next case's block rather
// than to `after`.
func (b *builder) switchCaseBodies(head model.BlockID, clauses []ast.Stmt, caseBlocks []model.BlockID, after model.BlockID, exprsOf func(*ast.CaseClause) []ast.Expr) {
	for i, c := range clauses {
		cc := c.(*ast.CaseClause)
		label := "default"
		if cc.List != nil {
			label = renderExprList(exprsOf(cc))
		}
		b.addEdge(head, caseBlocks[i], model.EdgeCase, label, b.spanOf(cc))
		b.current = caseBlocks[i]
		fallsThrough := false
		for j, stmt := range cc.Body {
			if br, ok := stmt.(*ast.BranchStmt); ok && br.Tok == token.FALLTHROUGH {
				fallsThrough = j == len(cc.Body)-1
				break
			}
			b.stmt(stmt, "")
		}
		if b.current == invalidBlock {
			continue
		}
		if fallsThrough && i+1 < len(caseBlocks) {
			b.addEdge(b.current, caseBlocks[i+1], model.EdgeFallthrough, "", b.spanOf(cc))
		} else {
			b.addEdge(b.current, after, model.EdgeNormal, "", model.Span{})
		}
	}
}

func (b *builder) selectStmt(x *ast.SelectStmt, label string) {
	head := b.ensureCurrent()
	after := b.newBlock("select-after")
	b.frames = append(b.frames, frame{label: label, isLoop: false, breakTo: after})

	for _, c := range x.Body.List {
		cc := c.(*ast.CommClause)
		caseLabel := "default"
		if cc.Comm != nil {
			caseLabel = renderCommClause(cc.Comm)
		}
		caseBlk := b.newBlock("comm-case")
		b.addEdge(head, caseBlk, model.EdgeCase, caseLabel, b.spanOf(cc))
		b.current = caseBlk
		b.stmtList(cc.Body)
		if b.current != invalidBlock {
			b.addEdge(b.current, after, model.EdgeNormal, "", model.Span{})
		}
	}
	b.frames = b.frames[:len(b.frames)-1]
	b.current = after
}

func (b *builder) branchStmt(x *ast.BranchStmt) {
	label := ""
	if x.Label != nil {
		label = x.Label.Name
	}
	switch x.Tok {
	case token.BREAK:
		if target, ok := b.resolveBreak(label); ok {
			b.addEdge(b.ensureCurrent(), target, model.EdgeBreak, label, b.spanOf(x))
		}
		b.current = invalidBlock
	case token.CONTINUE:
		if target, ok := b.resolveContinue(label); ok {
			b.addEdge(b.ensureCurrent(), target, model.EdgeContinue, label, b.spanOf(x))
		}
		b.current = invalidBlock
	case token.GOTO:
		if target, ok := b.labels[label]; ok {
			b.addEdge(b.ensureCurrent(), target, model.EdgeGoto, label, b.spanOf(x))
		}
		b.current = invalidBlock
	case token.FALLTHROUGH:
		// Handled directly in switchCaseBodies; a fallthrough reached here
		// is either misplaced (invalid Go, won't compile) or a case this
		// builder doesn't special-case yet. Treat as an opaque instruction
		// rather than silently dropping it.
		b.emit("fallthrough", x, "", nil, nil)
	}
}

func calleeName(info *types.Info, call *ast.CallExpr) string {
	if call == nil || info == nil {
		return ""
	}
	obj := calledObject(call.Fun, info)
	fn, ok := obj.(*types.Func)
	if !ok {
		if obj != nil {
			return obj.Name()
		}
		return ""
	}
	if fn.Pkg() == nil {
		return fn.Name()
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		recv := sig.Recv().Type()
		if ptr, ok := recv.(*types.Pointer); ok {
			recv = ptr.Elem()
		}
		if named, ok := recv.(*types.Named); ok && named.Obj().Pkg() != nil {
			return named.Obj().Pkg().Path() + "." + named.Obj().Name() + "." + fn.Name()
		}
	}
	return fn.Pkg().Path() + "." + fn.Name()
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

// renderExpr and its list/comm-clause variants produce a short, best-effort
// human-readable label for a condition or case, used only for dump/
// explanation output. They are not parsed back and carry no semantic
// weight of their own -- the graph structure (Edge.Kind, block topology) is
// what analysis reasons over.
func renderExpr(e ast.Expr) string {
	if e == nil {
		return ""
	}
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return renderExpr(x.X) + "." + x.Sel.Name
	case *ast.CallExpr:
		return renderExpr(x.Fun) + "(...)"
	case *ast.UnaryExpr:
		return x.Op.String() + renderExpr(x.X)
	case *ast.BinaryExpr:
		return renderExpr(x.X) + " " + x.Op.String() + " " + renderExpr(x.Y)
	case *ast.BasicLit:
		return x.Value
	case *ast.ParenExpr:
		return "(" + renderExpr(x.X) + ")"
	default:
		return "..."
	}
}

func renderExprList(exprs []ast.Expr) string {
	out := ""
	for i, e := range exprs {
		if i > 0 {
			out += ", "
		}
		out += renderExpr(e)
	}
	return out
}

func renderCommClause(s ast.Stmt) string {
	switch x := s.(type) {
	case *ast.SendStmt:
		return renderExpr(x.Chan) + " <- " + renderExpr(x.Value)
	case *ast.ExprStmt:
		return renderExpr(x.X)
	case *ast.AssignStmt:
		if len(x.Rhs) == 1 {
			return renderExpr(x.Rhs[0])
		}
		return "..."
	default:
		return fmt.Sprintf("%T", s)
	}
}
