package frontend

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"
)

func TestCollectSuppressionsRequiresAnchoredValidDirective(t *testing.T) {
	for _, tc := range []struct {
		name       string
		comment    string
		wantRules  []string
		wantReason string
	}{
		{name: "anchored rule", comment: "//lifeline:ignore LL1001", wantRules: []string{"LL1001"}},
		{name: "anchored rule with reason", comment: "//lifeline:ignore LL1001, ll1002 -- tracked in TICKET-123", wantRules: []string{"LL1001", "LL1002"}, wantReason: "tracked in TICKET-123"},
		{name: "bare directive with reason", comment: "// lifeline:ignore -- generated ownership bridge", wantRules: []string{"*"}, wantReason: "generated ownership bridge"},
		{name: "anchored block comment", comment: "/* lifeline:ignore LL1003 -- reviewed */", wantRules: []string{"LL1003"}, wantReason: "reviewed"},
		{name: "near miss prefix", comment: "//nolifeline:ignore LL1001"},
		{name: "directive after prose", comment: "// TODO lifeline:ignore LL1001"},
		{name: "near miss directive name", comment: "//lifeline:ignored LL1001"},
		{name: "unknown rule invalidates directive", comment: "//lifeline:ignore LL1001,LL9999"},
		{name: "reason needs separator", comment: "//lifeline:ignore LL1001 because flaky"},
		{name: "empty rule invalidates directive", comment: "//lifeline:ignore LL1001,"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "input.go", "package p\nvar _ = 1 "+tc.comment+"\n", parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			got := collectSuppressions(fset, []*ast.File{file})["input.go"][2]
			if tc.wantRules == nil {
				if len(got) != 0 {
					t.Fatalf("directives = %#v, want none", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("directives = %#v, want one", got)
			}
			if !reflect.DeepEqual(got[0].Rules, tc.wantRules) || got[0].Reason != tc.wantReason {
				t.Fatalf("directive = %#v, want rules %v reason %q", got[0], tc.wantRules, tc.wantReason)
			}
		})
	}
}
