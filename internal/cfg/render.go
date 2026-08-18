package cfg

import (
	"fmt"
	"strings"

	"github.com/gfedyukovich/lifeline/internal/model"
)

// RenderText renders g as a human-readable block listing:
//
//	func F
//
//	B0 (entry):
//	  ctx := ...
//	  go worker(ctx)
//	  -> B1
//
//	B1 (loop-header):
//	  -> B2 [true]
//	  -> B3 [false]
//
// Blocks are printed in ID order, which is construction (roughly source)
// order, not a topological or reachability order -- this is meant for a
// human comparing it against the source, not as a traversal API.
func RenderText(g *model.CFG) string {
	if g == nil {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "func %s\n\n", g.Function)
	for _, blk := range g.Blocks {
		label := fmt.Sprintf("B%d", blk.ID)
		if blk.Kind != "" {
			label += " (" + blk.Kind + ")"
		}
		fmt.Fprintf(&sb, "%s:\n", label)
		for _, instr := range blk.Instructions {
			line := instr.Op
			if instr.Callee != "" {
				line += " " + instr.Callee + "(...)"
			}
			fmt.Fprintf(&sb, "  %s\n", line)
		}
		if len(blk.Successors) == 0 {
			fmt.Fprintf(&sb, "  (no successors%s)\n", exitNote(g, blk.ID))
		}
		for _, e := range blk.Successors {
			target := fmt.Sprintf("B%d", e.To)
			if e.To == g.Exit {
				target = "EXIT"
			}
			if e.Label != "" {
				fmt.Fprintf(&sb, "  -> %s [%s: %s]\n", target, e.Kind, e.Label)
			} else {
				fmt.Fprintf(&sb, "  -> %s [%s]\n", target, e.Kind)
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func exitNote(g *model.CFG, id model.BlockID) string {
	if id == g.Exit {
		return ", this is EXIT"
	}
	return ""
}

// RenderDOT renders g as a Graphviz DOT digraph for external rendering
// (e.g. `dot -Tpng`). Entry and Exit are styled distinctly; loop-back edges
// are colored so cycles are visually obvious.
func RenderDOT(g *model.CFG) string {
	if g == nil {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "digraph %q {\n", g.Function)
	sb.WriteString("  node [shape=box, fontname=monospace];\n")
	for _, blk := range g.Blocks {
		name := dotNode(blk.ID)
		shape := ""
		switch blk.ID {
		case g.Entry:
			shape = ", style=filled, fillcolor=lightgreen"
		case g.Exit:
			shape = ", style=filled, fillcolor=lightcoral"
		}
		body := dotLabel(blk)
		fmt.Fprintf(&sb, "  %s [label=%q%s];\n", name, body, shape)
	}
	for _, blk := range g.Blocks {
		for _, e := range blk.Successors {
			attrs := fmt.Sprintf("label=%q", string(e.Kind))
			if e.Label != "" {
				attrs = fmt.Sprintf("label=%q", string(e.Kind)+": "+e.Label)
			}
			if e.Kind == model.EdgeLoopBack {
				attrs += ", color=red, style=dashed"
			}
			fmt.Fprintf(&sb, "  %s -> %s [%s];\n", dotNode(e.From), dotNode(e.To), attrs)
		}
	}
	sb.WriteString("}\n")
	return sb.String()
}

func dotNode(id model.BlockID) string {
	return fmt.Sprintf("B%d", id)
}

func dotLabel(blk model.BasicBlock) string {
	lines := []string{fmt.Sprintf("B%d %s", blk.ID, blk.Kind)}
	for _, instr := range blk.Instructions {
		line := instr.Op
		if instr.Callee != "" {
			line += " " + instr.Callee
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\\n")
}
