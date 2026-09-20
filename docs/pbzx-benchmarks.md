# PBZX decoding benchmarks and coverage

`BenchmarkPBZXDecode` compares sequential decoding with 1, 2, 4 and 8 workers
using the same XZ decoder and identical inputs. Each fixture contains eight
chunks at either 256 KiB or 16 MiB per chunk. Compressible chunks repeat bytes
0 through 250; mixed fixtures alternate those chunks with deterministic
xorshift32 bytes, seeded with 2463534242. The mixed fixture exercises stored
chunks as well as compressed chunks. All fixtures are generated locally; no
vendor download or private package is required.

Fixture generation and encoding happen outside the timed sub-benchmarks.
Each measured iteration constructs a reader, decodes the whole stream to
`io.Discard`, checks the decoded length, and closes the reader. Round-trip
tests separately check content. Results include ns/op, decoded MB/s, B/op
and allocs/op. B/op is cumulative allocation per iteration, not peak memory.

Run all cases, taking three samples with eight available Go processors:

```sh
go version
GOMAXPROCS=8 go test ./pkg/pbzx -run '^$' \
  -bench '^BenchmarkPBZXDecode$' -benchmem -benchtime=1s -count=3
```

A shorter reproducible run of the small fixtures:

```sh
GOMAXPROCS=8 go test ./pkg/pbzx -run '^$' \
  -bench '^BenchmarkPBZXDecode/block=262144/' -benchmem -benchtime=3x -count=3
```

Record the CPU model, RAM, operating system, Go version, and `GOMAXPROCS`
alongside any published results. Compare medians from the same fixture and
machine. A speedup is not a guarantee for other codecs, input shapes, or CPUs.
Process-level peak RSS measurements include fixture setup and the Go runtime;
they must not be described as decoder-only memory or substituted for B/op.

## Coverage gate

For PR #62, measure the full contribution against its original base, including
the submitted code and subsequent improvements:

```sh
go test -race -covermode=atomic -coverprofile=coverage.out ./pkg/pbzx ./pkg/xar
python3 scripts/check_diff_coverage.py \
  --base a26bfff245bcc243e296a8be75c1d9c187dfaad8 \
  --profile coverage.out --minimum 95
python3 -m unittest discover -s scripts -p test_diff_coverage.py
```

The gate counts Go-instrumented statement blocks intersecting added or
modified production-code lines, once per block. A block is covered when its
execution count is positive. This is statement-block coverage, not branch
coverage; it can include unchanged statements sharing a changed block.
Tests, benchmarks, and scripts are excluded. Untracked production Go files
are included. Missing profile entries for changed implementation files fail
the gate. The output gives covered/total statements per file and overall,
and the source ranges of uncovered blocks. Package-wide coverage is a
different metric and is not used to claim the 95% changed-code target.

For a different contribution, pass its base commit and generate a fresh
profile covering every changed implementation package. Regenerate the
profile after source changes so its line numbers match the diff.

## Review measurements (20 September 2026)

Environment: Apple M4, 24 GiB RAM, macOS 27.0, Go 1.27.1 darwin/arm64,
`GOMAXPROCS=8`. These are medians of three samples, each with three measured
iterations, using the complete fixture matrix:

```sh
GOMAXPROCS=8 go test ./pkg/pbzx -run '^$' \
  -bench '^BenchmarkPBZXDecode$' -benchmem -benchtime=3x -count=3
```

| Chunk size | Input | Reader | ms/op | MB/s | MiB allocated/op | allocs/op |
|---|---|---|---:|---:|---:|---:|
| 256 KiB | Compressible | Sequential | 5.247 | 399.71 | 64.34 | 168 |
| 256 KiB | Compressible | Workers: 1 | 5.274 | 397.63 | 64.60 | 204 |
| 256 KiB | Compressible | Workers: 2 | 3.119 | 672.36 | 64.85 | 207 |
| 256 KiB | Compressible | Workers: 4 | 2.060 | 1018.23 | 65.35 | 217 |
| 256 KiB | Compressible | Workers: 8 | 1.823 | 1150.16 | 66.35 | 237 |
| 256 KiB | Mixed | Sequential | 2.763 | 758.89 | 32.17 | 102 |
| 256 KiB | Mixed | Workers: 1 | 2.746 | 763.61 | 32.67 | 136 |
| 256 KiB | Mixed | Workers: 2 | 2.662 | 787.95 | 32.93 | 143 |
| 256 KiB | Mixed | Workers: 4 | 1.772 | 1183.80 | 33.68 | 148 |
| 256 KiB | Mixed | Workers: 8 | 1.328 | 1579.13 | 35.18 | 173 |
| 16 MiB | Compressible | Sequential | 263.557 | 509.25 | 64.34 | 167 |
| 16 MiB | Compressible | Workers: 1 | 268.650 | 499.60 | 80.35 | 200 |
| 16 MiB | Compressible | Workers: 2 | 135.720 | 988.93 | 96.35 | 203 |
| 16 MiB | Compressible | Workers: 4 | 70.612 | 1900.79 | 128.36 | 213 |
| 16 MiB | Compressible | Workers: 8 | 44.126 | 3041.71 | 192.37 | 235 |
| 16 MiB | Mixed | Sequential | 132.697 | 1011.46 | 32.17 | 102 |
| 16 MiB | Mixed | Workers: 1 | 140.964 | 952.14 | 64.18 | 137 |
| 16 MiB | Mixed | Workers: 2 | 137.517 | 976.01 | 80.18 | 138 |
| 16 MiB | Mixed | Workers: 4 | 69.762 | 1923.95 | 128.18 | 148 |
| 16 MiB | Mixed | Workers: 8 | 38.865 | 3453.45 | 224.19 | 163 |

For the 16 MiB chunks, four workers were about 3.73 times faster on the
compressible fixture and 1.90 times faster on the mixed fixture. Their
cumulative allocations increased from approximately 64 to 128 MiB and from
32 to 128 MiB respectively. Eight workers allocated more again. Small chunks
still pay for the encoded XZ dictionary, explaining their high allocation
cost relative to decoded size. These measurements expose the memory/throughput
tradeoff; they are not peak RSS measurements or CLI timings.

## Review validation

The branch starts at submitted PR head
`56326027c7e00c701bfc5a925adbb55496f68596`. The race-enabled profile measured
**356/367 changed statements (97.00%)** against the PR base, above the 95%
minimum. The scope includes the original PBZX and XAR changes as well as the
hardening changes.

| Changed implementation | Covered/total | Coverage |
|---|---:|---:|
| PBZX buffered decoding | 69/69 | 100.00% |
| PBZX concurrent decoding | 121/127 | 95.28% |
| PBZX shared reader | 91/94 | 96.81% |
| XAR decoding changes | 7/7 | 100.00% |
| XAR streaming verification | 68/70 | 97.14% |

Validation passed:

- `go test ./pkg/... ./internal/...`
- `go test -race -covermode=atomic -coverprofile=coverage.out ./pkg/pbzx ./pkg/xar`
- The 95% diff coverage gate and its five Python unit tests.
- `go vet ./...` and `golangci-lint run ./...` (zero issues).
- `go test ./acceptance -run 'Test(BuildPBZX|PBZX|BuildLZFSE|PkgutilReadsOurLZFSEPayload)' -count=1 -timeout=3m`
- `CGO_ENABLED=0 go build ./...` for linux, darwin and windows, each on amd64 and arm64.
- All 20 benchmark cases, with three samples each.

The exact review reproductions were checked again: the valid 104-byte PBZX
input now decodes to 1 KiB successfully, and the malformed 804-byte PBZE input
returns an invalid-state error through the concurrent reader without a panic.
