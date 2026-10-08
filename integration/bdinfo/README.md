# Development BDInfo bridge

This separate GPL-2.0-or-later module validates report-plus-HDR10+ collection
against the uncommitted issue-45 API in the neighboring `go-bdinfo` checkout.
The root MIT module neither imports nor requires it. Its relative development
replacements require this layout:

```text
E:\github\go-hdr10-plus
E:\github\go-bdinfo
```

There is no immutable issue-45 release to pin yet. These replacements are
development inputs, not a reproducible released dependency. Publication must
replace them with verified immutable versions. The authorized local BDInfo patch
adds observed EOF packet counts and bounded original PES headers, and validates
adaptation syntax and bounded PES payload stuffing on its optional collection
path. Existing report parsing is
unchanged; the original uncommitted checkout is preserved in validation receipts.

`Scan(ctx, Options{BDInfo: bdinfo.Options{...}})` configures a synchronous
consumer and performs one `bdinfo.Run`. It retains normally completed physical
clips, then assembles the selected public timelines. Each playlist outcome
contains either an extraction or an explicit error; source collection errors
are separate from ordinary report errors. Applications call `PlotMetadata` and
the root `Render` themselves. Report-only consumers continue to call BDInfo
without a consumer.

The development acceptance test covers a synthetic folder, two playlists
sharing a physical stream, repeated occurrences, original zero/nonmonotonic
PTS, full delivered ES totals, report parity, and visible budget exhaustion.
Primary ordinary M2TS mappings and valid STC associations are required. Conditions
1, 5 and 6 select reset or candidate seamless replay; retained HEVC reference and
output syntax must establish the actual splice. Unknown or unprovable joins fail.
Generated folder/UDF ISO tests cover shared/repeated clips and seamless cuts.
A stream larger than 5 MiB verifies full delivered ES totals. `PeakRetainedBytes`
reports peak accounted extraction storage; it is checked against the shared quota.
Packet epoch boundaries must align with original PES starts, physical start or
observed clean EOF. An STC boundary inside a PES cannot be proved from sparse
markers and fails explicitly. Bounded original PES headers reach core preflight
before they are discarded, preventing hidden trick modes or malformed optional
fields from receiving a completed extraction.
Public regression controls verify that malformed bounded PES tails, both in the
terminal packet and later continuation packets, cannot publish clean EOF or a
source packet extent. Legal 0xff stuffing preserves ES delivery and report output.

The public scanner API exposes unique consumer registrations and delivered ES
bytes and observed physical packet counts, but no injectable reader or physical
read-call counters. EOF counts include unselected and adaptation-only packets;
they certify an extent and do not count IO calls. The bridge calls
`Run` once and never opens media itself. These tests verify delivery and reuse;
instrumented full-read counts, HDD/network/NVMe performance and released dependency
validation require additional upstream instrumentation or a separately instrumented
application. They are not inferred from consumer counts.

The default scan budget is 1 GiB across core collectors, completed clips,
bookkeeping, assembly scratch and all retained playlist outputs. Declines after
exhaustion use an aggregate count and at most 16 shortened identity samples.
Budget failure never triggers another scan. Cross-scan memory and persistence
are caller owned; a report cache cannot reconstruct missing frame metadata.

From this module directory, run `go test ./...`, `go test -race ./...` and
`go vet ./...` separately from root checks.

`go test -run '^$' -bench '^BenchmarkScan$' -benchmem -count 5` compares cached
synthetic report and report-plus-extraction runs. Set
`HDR10PLUS_BDINFO_BENCHMARK_PATH` to a caller-owned legal folder/ISO for opt-in
measurements. Record media hashes, cache/storage conditions and matching builds;
these benchmarks exclude PNG rendering and do not emulate cold storage.

The disc test metadata fixture is adapted from go-bdinfo's local
`pkg/bdinfo/collection_test.go`, Copyright (c) 2026, s0up and the autobrr
contributors, GPL-2.0-or-later. The module's LICENSE contains GPL version 2;
source SPDX identifiers permit that version or any later version.
