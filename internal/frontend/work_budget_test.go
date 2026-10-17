package frontend

import (
	"fmt"
	"testing"
)

func buildWithWorkStats(t *testing.T, source string) WorkStats {
	t.Helper()
	fixture := newBenchmarkFixture(t, source)
	var stats WorkStats
	fixture.in.WorkStats = &stats
	program, err := Build(fixture.in, fixture.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if program.Truncated {
		t.Fatal("work-budget fixture unexpectedly hit max_functions")
	}
	return stats
}

// These budgets intentionally constrain algorithmic work counts rather than
// elapsed time. They stay stable across CI machines while detecting the exact
// F11 regressions the benchmark harness was added to guard against: whole-
// package fixed-point rescans, repeated structural CFG construction, and
// WaitGroup accounting that revisits unrelated groups' statements.
func TestFrontendWorkBudgets(t *testing.T) {
	t.Run("parameter-delegation-chain", func(t *testing.T) {
		const links = 200
		stats := buildWithWorkStats(t, paramDelegationChainSource(links))
		functions := links + 1
		// The combined parameter/Done solver can grow two monotone summaries in
		// separate waves, so the adversarial chain needs at most about three
		// evaluations per selected function.
		if max := 3*functions + 4; stats.ParameterEffectEvaluations > max {
			t.Fatalf("parameter worklist evaluated %d functions; budget %d (functions=%d)", stats.ParameterEffectEvaluations, max, functions)
		}
		if stats.StructuralCFGBuilds > functions {
			t.Fatalf("built %d structural CFGs for %d functions; cache should build at most once per body", stats.StructuralCFGBuilds, functions)
		}
	})

	t.Run("constructor-forwarding-chain", func(t *testing.T) {
		const wrappers = 200
		stats := buildWithWorkStats(t, constructorForwardingSource(wrappers))
		// use + wrappers + New
		functions := wrappers + 2
		if max := 2*functions + 4; stats.ConstructorEvaluations > max {
			t.Fatalf("constructor worklist evaluated %d functions; budget %d (functions=%d)", stats.ConstructorEvaluations, max, functions)
		}
		if stats.StructuralCFGBuilds > functions {
			t.Fatalf("built %d structural CFGs for %d functions; cache should build at most once per body", stats.StructuralCFGBuilds, functions)
		}
	})

	t.Run("independent-waitgroups", func(t *testing.T) {
		for _, groups := range []int{20, 80} {
			t.Run(fmt.Sprintf("groups=%d", groups), func(t *testing.T) {
				stats := buildWithWorkStats(t, manyWaitGroupsSource(groups))
				// Each group should visit only its own small statement cluster in
				// the whole-function and round passes. A groups*body rescan would
				// grow quadratically and exceed this linear budget quickly.
				if max := 8*groups + 16; stats.GroupBalanceStatementVisits > max {
					t.Fatalf("WaitGroup accounting visited %d statements; linear budget %d for %d groups", stats.GroupBalanceStatementVisits, max, groups)
				}
			})
		}
	})
}

func TestWorkStatsResetPerBuild(t *testing.T) {
	fixture := newBenchmarkFixture(t, paramDelegationChainSource(8))
	stats := WorkStats{ParameterEffectEvaluations: 1_000_000, ConstructorEvaluations: 1_000_000, StructuralCFGBuilds: 1_000_000, GroupBalanceStatementVisits: 1_000_000}
	fixture.in.WorkStats = &stats
	if _, err := Build(fixture.in, fixture.cfg); err != nil {
		t.Fatal(err)
	}
	if stats.ParameterEffectEvaluations >= 1_000_000 || stats.StructuralCFGBuilds >= 1_000_000 {
		t.Fatalf("Build did not reset WorkStats: %+v", stats)
	}
}
