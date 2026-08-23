module github.com/gfedyukovich/lifeline/benchmarks/phase4/cross_package_mixed_loop_resolution/consumer

go 1.25

require github.com/gfedyukovich/lifeline/benchmarks/phase4/cross_package_mixed_loop_resolution/worker v0.0.0

replace github.com/gfedyukovich/lifeline/benchmarks/phase4/cross_package_mixed_loop_resolution/worker => ../worker
