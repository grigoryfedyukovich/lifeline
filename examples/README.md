# Examples

Each directory is a small Go package that demonstrates one lifecycle protocol, or one analyzer edge case. Build the analyzer first (`go build -o ./bin/lifeline ./cmd/lifeline`), then run an example from the repository root:

```bash
./bin/lifeline ./examples/lost_cancel
./bin/lifeline -config ./examples/custom_context_wrapper/lifeline.yaml ./examples/custom_context_wrapper
```

Examples that ship a `lifeline.yaml` need `-config` to show their intended behavior. Cross-package examples are meant for `go vet -vettool="$(pwd)/bin/lifeline" ./examples/<name>/...`.

The **Expected** column lists results asserted by `tests/integration`. Examples without one are minimized fixtures; run them to see the current output, and see the [tutorial](../docs/tutorial.md) for a guided walk-through of the core protocols.

## Core protocols

| Example | Config | Expected |
|---|---|---|
| [`channel_shutdown`](channel_shutdown/) |  | no diagnostics |
| [`ignored_context`](ignored_context/) |  | LL1002 |
| [`lost_cancel`](lost_cancel/) |  | LL1001 |
| [`proper_cancel`](proper_cancel/) |  | no diagnostics |
| [`proper_context`](proper_context/) |  | no diagnostics |
| [`proper_errgroup`](proper_errgroup/) |  | no diagnostics |
| [`proper_waitgroup`](proper_waitgroup/) |  | no diagnostics |
| [`uncalled_cancel`](uncalled_cancel/) |  | LL1001 |
| [`unjoined_errgroup`](unjoined_errgroup/) |  | LL1004 |
| [`unjoined_waitgroup`](unjoined_waitgroup/) |  | LL1003 |
| [`waitgroup_second_round_unjoined`](waitgroup_second_round_unjoined/) |  | LL1003 |

## Configuration, wrappers, and suppression

| Example | Config | Expected |
|---|---|---|
| [`context_wrapper_error_checked`](context_wrapper_error_checked/) | `lifeline.yaml` | no diagnostics |
| [`context_wrapper_error_early_return`](context_wrapper_error_early_return/) | `lifeline.yaml` | LL1001 |
| [`context_wrapper_typed_results`](context_wrapper_typed_results/) | `lifeline.yaml` |  |
| [`custom_context_wrapper`](custom_context_wrapper/) | `lifeline.yaml` | no diagnostics without config; LL1001 with config |
| [`custom_start_wrapper`](custom_start_wrapper/) | `lifeline.yaml` | no diagnostics without config; LL1002 with config |
| [`generated_exclusion`](generated_exclusion/) |  |  |
| [`ignore_path`](ignore_path/) | `lifeline.yaml` |  |
| [`inline_suppression`](inline_suppression/) |  |  |
| [`start_wrapper_declared_target`](start_wrapper_declared_target/) | `lifeline.yaml` |  |
| [`start_wrapper_inline_priority`](start_wrapper_inline_priority/) | `lifeline.yaml` |  |

## Run status and coverage

| Example | Config | Expected |
|---|---|---|
| [`coverage_unsupported`](coverage_unsupported/) |  |  |
| [`status_fully_modeled`](status_fully_modeled/) |  |  |
| [`status_truncated_unsupported`](status_truncated_unsupported/) |  |  |
| [`status_unsupported_only`](status_unsupported_only/) |  |  |

## Cross-package targets (`go vet` facts)

| Example | Config | Expected |
|---|---|---|
| [`cross_package_context`](cross_package_context/) |  |  |
| [`cross_package_mixed_loop_resolution`](cross_package_mixed_loop_resolution/) |  |  |

## Cancellation and group ownership (fields, constructors, helpers, closures)

| Example | Config | Expected |
|---|---|---|
| [`cancel_direct_consumed`](cancel_direct_consumed/) |  |  |
| [`cancel_direct_not_consumed`](cancel_direct_not_consumed/) |  |  |
| [`cancel_stored_struct`](cancel_stored_struct/) |  | LL1001 |
| [`cancel_stored_struct_consumed`](cancel_stored_struct_consumed/) |  | no diagnostics |
| [`cancel_three_hop_caller_first`](cancel_three_hop_caller_first/) |  | LL1001 |
| [`cancel_three_hop_consumed`](cancel_three_hop_consumed/) |  | no diagnostics |
| [`closure_local_cancel`](closure_local_cancel/) |  | LL1001 |
| [`constructor_cancel_handle_dropped`](constructor_cancel_handle_dropped/) |  | LL1001 |
| [`constructor_conditional_consume`](constructor_conditional_consume/) |  | LL1001 |
| [`constructor_consumed`](constructor_consumed/) |  |  |
| [`constructor_consumed_through_alias`](constructor_consumed_through_alias/) |  |  |
| [`constructor_copy_consume`](constructor_copy_consume/) |  |  |
| [`constructor_opaque_after_field`](constructor_opaque_after_field/) |  |  |
| [`constructor_opaque_escape`](constructor_opaque_escape/) |  | LL1001 |
| [`constructor_pointer_alias_consume`](constructor_pointer_alias_consume/) |  |  |
| [`constructor_waitgroup_handle_dropped`](constructor_waitgroup_handle_dropped/) |  | LL1003 |
| [`field_alias_consumed`](field_alias_consumed/) |  | no diagnostics |
| [`field_alias_never_consumed`](field_alias_never_consumed/) |  | LL1001 |
| [`field_alias_unused`](field_alias_unused/) |  |  |
| [`field_conditional_consume`](field_conditional_consume/) |  | LL1001 |
| [`field_overwrite_old_binding`](field_overwrite_old_binding/) |  | LL1001 |
| [`field_pointer_alias`](field_pointer_alias/) |  |  |
| [`field_reassigned_unconsumed_old`](field_reassigned_unconsumed_old/) |  | LL1001 |
| [`method_field_consume`](method_field_consume/) |  | no diagnostics |
| [`method_one_field_one_leak`](method_one_field_one_leak/) |  | LL1001 |
| [`shared_cancel_two_fields`](shared_cancel_two_fields/) |  |  |
| [`shared_waitgroup_fields`](shared_waitgroup_fields/) |  | LL1003 |
| [`waitgroup_direct_consumed`](waitgroup_direct_consumed/) |  |  |
| [`waitgroup_direct_not_consumed`](waitgroup_direct_not_consumed/) |  |  |
| [`waitgroup_stored_struct`](waitgroup_stored_struct/) |  | LL1003 |
| [`waitgroup_stored_struct_waited`](waitgroup_stored_struct_waited/) |  | no diagnostics |
| [`waitgroup_three_hop_caller_first`](waitgroup_three_hop_caller_first/) |  |  |
| [`waitgroup_three_hop_consumed`](waitgroup_three_hop_consumed/) |  |  |

## Join order and balance (`LL1003`-`LL1005`)

| Example | Config | Expected |
|---|---|---|
| [`defer_cancel_before_wait`](defer_cancel_before_wait/) |  | LL1005 |
| [`defer_cancel_with_explicit_stop`](defer_cancel_with_explicit_stop/) |  | no diagnostics |
| [`join_add_in_loop_wait_after`](join_add_in_loop_wait_after/) |  |  |
| [`join_branch_wait_before_return`](join_branch_wait_before_return/) |  |  |
| [`join_conditional_wait_missing`](join_conditional_wait_missing/) |  |  |
| [`join_done_on_wrong_group`](join_done_on_wrong_group/) |  |  |
| [`join_helper_wait`](join_helper_wait/) |  |  |
| [`join_multiple_adds_one_wait`](join_multiple_adds_one_wait/) |  |  |
| [`join_owner_return_branch`](join_owner_return_branch/) |  |  |
| [`join_two_groups_mixed`](join_two_groups_mixed/) |  |  |
| [`join_wait_before_add`](join_wait_before_add/) |  |  |
| [`stop_before_wait`](stop_before_wait/) |  |  |
| [`waitgroup_add_done_imbalance`](waitgroup_add_done_imbalance/) |  |  |
| [`waitgroup_conditional_start_unconditional_wait`](waitgroup_conditional_start_unconditional_wait/) |  |  |
| [`waitgroup_conditional_wait`](waitgroup_conditional_wait/) |  |  |
| [`waitgroup_wait_before_add`](waitgroup_wait_before_add/) |  |  |

## Loop exits and control flow (`LL1002`)

| Example | Config | Expected |
|---|---|---|
| [`break_loop_then_unstoppable`](break_loop_then_unstoppable/) |  |  |
| [`break_then_unstoppable`](break_then_unstoppable/) |  |  |
| [`channel_stop_loop_then_unstoppable`](channel_stop_loop_then_unstoppable/) |  |  |
| [`channel_stop_then_unstoppable`](channel_stop_then_unstoppable/) |  |  |
| [`conditional_ctx_done`](conditional_ctx_done/) |  |  |
| [`conditional_return_after_ctx_done`](conditional_return_after_ctx_done/) |  |  |
| [`conditional_return_in_stop_case`](conditional_return_in_stop_case/) |  |  |
| [`conditional_stop_case`](conditional_stop_case/) |  |  |
| [`context_stop_then_unstoppable`](context_stop_then_unstoppable/) |  |  |
| [`ctx_done_return`](ctx_done_return/) |  |  |
| [`ctx_stop_loop_then_unstoppable`](ctx_stop_loop_then_unstoppable/) |  |  |
| [`dead_break_then_continue`](dead_break_then_continue/) |  |  |
| [`dead_return_then_continue`](dead_return_then_continue/) |  |  |
| [`dead_stop_then_continue`](dead_stop_then_continue/) | `lifeline.yaml` |  |
| [`direct_break`](direct_break/) |  |  |
| [`direct_break_resolves`](direct_break_resolves/) |  |  |
| [`goto_skips_return`](goto_skips_return/) |  |  |
| [`goto_skips_stop`](goto_skips_stop/) | `lifeline.yaml` |  |
| [`labeled_outer_break`](labeled_outer_break/) |  |  |
| [`loop_break_scope`](loop_break_scope/) |  |  |
| [`loop_prior_return`](loop_prior_return/) |  |  |
| [`nested_break_not_outer`](nested_break_not_outer/) |  |  |
| [`nested_function_return`](nested_function_return/) |  |  |
| [`nested_inner_break_then_unreachable_after`](nested_inner_break_then_unreachable_after/) |  |  |
| [`nested_labeled_break_not_outer`](nested_labeled_break_not_outer/) |  |  |
| [`nested_range_break_not_outer`](nested_range_break_not_outer/) |  |  |
| [`return_then_unstoppable`](return_then_unstoppable/) |  |  |
| [`safe_then_unstoppable_loop`](safe_then_unstoppable_loop/) |  |  |
| [`single_unresolved_loop`](single_unresolved_loop/) |  |  |
| [`two_stop_signals_one_dead`](two_stop_signals_one_dead/) |  |  |
| [`unreachable_break`](unreachable_break/) |  |  |
| [`unreachable_break_in_loop`](unreachable_break_in_loop/) |  |  |
| [`unreachable_return`](unreachable_return/) |  |  |
| [`unreachable_return_in_loop`](unreachable_return_in_loop/) |  |  |
| [`unreachable_stop`](unreachable_stop/) |  |  |
| [`unreachable_stop_wrapper`](unreachable_stop_wrapper/) |  |  |

## Recent-feature experiment corpus

The 27 cases in [corpus.json](corpus.json) add paired warning/clean controls for
recent changes: local `var` cancellation, constant conditions, asynchronous
joins, closure invocation, nested goroutines, richer stop-signal correlation,
helper/wrapper join ordering, handoff status, and imported constructors.
The manifest records the relevant commits and expected rules/status; ordinary
integration tests enforce those expectations.

Run `make experiments EXPERIMENT_OUT=/tmp/lifeline-experiments-current` for all
examples plus `net/http`, or use `scripts/run_experiments.py --features-only`
with `--binary` and a new `--output` directory for just the additions. See the
[evaluation protocol](../docs/evaluation.md#repeating-the-examples-and-recent-feature-experiments)
for repeated timings, provenance, and interpreting unsupported results.
