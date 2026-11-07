# Event stream benchmarks

`service/events_bench_test.go` measures the cost of event ids and the replay buffer ([The event stream resumes from a shared replay buffer](../../adr/2026-11-07-the-event-stream-resumes-from-a-shared-replay-buffer.md)).

## Run

```sh
GOWORK=off go test ./service -run '^$' -bench BenchmarkEvents -benchmem -count 10 > new.txt
benchstat old.txt new.txt
```

Install `benchstat` with `go install golang.org/x/perf/cmd/benchstat@latest`. Other agents may be loading the machine: run `uptime` before and after, and record the load with the result.

To compare `publish` with a commit that predates the replay buffer, add a temporary worktree at that commit with `git worktree add --detach`. Copy in this file, keeping only `benchmarkTrafficRequest`, `drainSubscribers`, `benchmarkPublish` and the `publish/subscribers` cases, because the other cases use the buffer. Run the same command there, then remove the worktree.

## Cases

| Case | What it measures |
| --- | --- |
| `publish/subscribers=N` | One `traffic.request` with realistic metadata, including JSON encoding, published to N subscribers that drain their queues. The timer pauses every 256 publishes until the queues empty, so no subscriber overflows. |
| `publish/buffer=full` | The same with a full replay buffer, so every publish evicts the oldest event. |
| `resume/behind=N` | From sending `GET /events` with `Last-Event-ID` to reading the last of N replayed events, over loopback. `p95-ms` is the 95th percentile. |
| `buffer/memory` | Heap held by a broadcaster whose buffer is full of alternating `traffic.request` and `traffic.response` events with metadata, in `MiB`. `B/op` for this case is the setup's allocation, not what is held. |

## Targets

| Target | Limit |
| --- | --- |
| `publish/subscribers=10` time | at most 1.5× its time before the replay buffer |
| `publish/subscribers=10` allocations | at most 1 more alloc/op than before the replay buffer |
| `resume/behind=4096` p95 | under 50 ms on loopback |
| `buffer/memory` | under 16 MiB |

## Baseline

Measured 2026-11-07 at 12:34 to 12:37 GST on an Apple M2 (8 cores, 24 GiB, macOS 27.0.1), Go 1.26.0. Other agents were using the machine: the 1-minute load average was 4.35 to 5.23 and the 5-minute 3.45 to 4.02. "Before" is commit 7d411bd (`main`); "after" is the commit that added the replay buffer. Values are benchstat medians of 10 runs.

| Case | Before | After | Target | Result |
| --- | --- | --- | --- | --- |
| `publish/subscribers=0` | 4.455 µs, 45 allocs | 4.076 µs, 45 allocs | none | |
| `publish/subscribers=1` | 5.359 µs, 45 allocs | 5.084 µs, 45 allocs | none | |
| `publish/subscribers=10` | 6.032 µs, 45 allocs | 5.745 µs, 45 allocs | ≤ 9.048 µs (1.5×), ≤ 46 allocs | pass (0.95×, +0 allocs) |
| `publish/subscribers=50` | 9.644 µs, 45 allocs | 9.407 µs, 45 allocs | none | |
| `publish/buffer=full` | | 4.101 µs, 45 allocs | none | |
| `resume/behind=100` | | 229 µs mean, p95 0.33 ms | none | |
| `resume/behind=1000` | | 1.02 ms mean, p95 1.31 ms | none | |
| `resume/behind=4096` | | 3.81 ms mean, p95 4.15 ms | p95 < 50 ms | pass |
| `buffer/memory` | | 2.33 MiB | < 16 MiB | pass |

`publish` measured 2 to 9 percent faster after the change. The new code replaces a `select` per subscriber with a length check, but the two runs were also taken a few minutes apart under changing load, so read this as "no slower", not as a speed-up. The event id adds no allocation because `publish` stores only the sequence number, and the handler formats the id as it writes the frame.
