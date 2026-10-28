package cfg

import "testing"

func TestGroupedDeclarationInitializerOrder(t *testing.T) {
	_, sites, _, body := buildWithCallSites(t, "package p\nfunc acquire()int{return 0};func cleanup()int{return 0}\nfunc F(){var(a=acquire();b=cleanup());_,_=a,b}", "F")
	a, okA := sites[callNamed(t, body, "acquire")]
	b, okB := sites[callNamed(t, body, "cleanup")]
	if !okA || !okB || a.Block != b.Block || a.Index >= b.Index {
		t.Fatalf("acquire=%+v(%v) cleanup=%+v(%v)", a, okA, b, okB)
	}
}
