# go-hdr10-plus

A pure-Go HDR10+ brightness plotting library for Go 1.27 or newer. Decode
upstream-style metadata JSON or supply Go values, choose a peak estimator and an
inclusive frame crop, and render an opaque 3000 × 1200 image in memory.

```powershell
go get github.com/Audionut/go-hdr10-plus
```

```go
package application

import (
    "bytes"
    "image/png"

    hdr10plus "github.com/Audionut/go-hdr10-plus"
)

func plotPNG(input []byte) ([]byte, error) {
    metadata, err := hdr10plus.DecodeJSON(bytes.NewReader(input))
    if err != nil {
        return nil, err
    }
    img, err := hdr10plus.Render(metadata, hdr10plus.Options{
        Title: "My HDR10+ plot",
        PeakSource: hdr10plus.PeakHistogram,
    })
    if err != nil {
        return nil, err
    }
    var output bytes.Buffer
    if err := png.Encode(&output, img); err != nil {
        return nil, err
    }
    return output.Bytes(), nil
}
```

For direct input, use `Metadata{Profile: "A", SceneCount: 1, Frames: []Frame{...}}`.
`AverageRGB`, `DistributionValues`, and `MaxSCL` are unsigned integers in **0.1-nit
units**: `1000` represents 100 nits. Metadata is a plotting model; marshaling it
does not produce the upstream wire format. SceneCount must be nonnegative and
always describes the whole input. Profile is a display label, including an empty
string if provided.

`Options{}` uses title `HDR10+ plot`, `PeakHistogram`, and all frames. An empty
title selects the default. `Range: &FrameRange{Start: 20, End: 40}` selects 21
frames by array position and rebases their x coordinates to zero. A nonnil
`FrameRange{0, 0}` selects the first frame. The frame count and legend statistics
describe the crop; the profile and scene count describe the full input.

| Peak source | Calculation |
| --- | --- |
| `PeakHistogram` | Maximum supplied distribution value / 10 |
| `PeakHistogram99` | Last supplied distribution value / 10, preserving order |
| `PeakMaxSCL` | Maximum supplied component / 10; any nonempty list |
| `PeakMaxSCLLuminance` | `(0.2627*R + 0.678*G + 0.0593*B) / 10`; exactly three RGB components |

The y axis is linear in ST 2084 PQ codes with tick labels in nits. MaxCLL and
MaxFALL follow the upstream plot's maximum peak and average series terminology.
Each legend `avg` is **inverse PQ of the mean PQ code**, rather than arithmetic
mean nits. Values above 10,000 nits remain in statistics and are clipped only at
the chart viewport. An average above its peak remains valid. Single-frame plots
include an opaque two-pixel sample marker.

`DecodeJSON` accepts exactly one JSON object followed by optional whitespace. It
requires a nonnull string at `JSONInfo.HDR10plusProfile`, a nonempty `SceneInfo`
array of objects containing `LuminanceParameters.AverageRGB`, and a nonnull
`SceneInfoSummary.SceneFrameNumbers` array. AverageRGB is uint32; scene frame
numbers are uint64 and only their array length is retained. Zero values and an
empty scene summary are valid. Null numeric elements, negative/fractional
numbers, overflow, invalid containers, and trailing JSON values are errors.

`MaxScl` and `LuminanceDistributions.DistributionValues` may be missing, null, or
empty when unused. Every supplied plotting field is checked during decoding;
rendering requires the selected peak source only for selected frames. Unconsumed
extensions, including DistributionIndex, are ignored. Standard `encoding/json`
case matching and duplicate processing apply. This plotting projection accepts
smaller documents than upstream; it is not full HDR10+ profile/schema validation.

Classify errors with `errors.Is(err, hdr10plus.ErrInvalidMetadata)` or
`errors.Is(err, hdr10plus.ErrInvalidOptions)`. Original JSON errors remain
available to `errors.As`, and reader failures retain their identity. Error
wording is not an API guarantee. Failed operations return nil results.

The library does not close readers, open files, log, start goroutines, or mutate
inputs. Every image owns its pixels. Concurrent renders may share unchanged
metadata; callers must not mutate it while rendering. Applications own PNG
encoding and handle writer errors. There is no CLI in the plotting package,
resampling, arbitrary sizing, or alternate output format.

Both operations are synchronous and have no cancellation parameter. Applications
own reader size limits and deadlines; `io.LimitReader` can bound input, with
truncated JSON returning an error. Decode memory grows with input/frame/peak
entry count. Render uses O(selected frames) samples, about 14.4 MB for the canvas,
and bounded raster buffers. It does not downsample long inputs.

Representative render measurements on an Intel Core i5-14600K (14 cores, 20
logical processors), Windows amd64, Go 1.27.1, using `go test -run '^$' -bench .
-benchmem ./...`. Inputs vary average brightness with constant histogram peaks;
setup and PNG encoding are excluded. These are local measurements, not latency
guarantees:

| Selected frames | Time/render | Allocated bytes/render | Allocations/render |
| --- | --- | --- | --- |
| 259 | 49.3 ms | 35,531,855 | 185 |
| 100,000 | 75.9 ms | 37,133,525 | 235 |
| 1,000,000 | 421.9 ms | 51,536,386 | 234 |

Numerical behavior is based on
[quietvoid/hdr10plus_tool revision 82cb01b](https://github.com/quietvoid/hdr10plus_tool/tree/82cb01ba387184476dbf5fa3bddebf0a345693d7).
Go Regular is embedded, so text metrics and rasterization differ from Rust's
fonts. An additional 20 pixels separate the y description from tick labels for
readability. X ticks use the smallest `1, 2, 5 × 10^k` interval at least
`max(1, ceil(frameCount/24))`. Exact text baselines and PNG bytes are not parity
guarantees. Numeric references and decoded-pixel Go goldens are independent.

Development checks:

```powershell
go mod verify
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
go test -run '^$' -fuzz '^FuzzDecodeJSON$' -fuzztime 30s .
go test -run '^$' -bench . -benchmem ./...
$env:CGO_ENABLED = '0'
go test ./...
```

See [test fixture provenance](testdata/PROVENANCE.md) for the Rust oracle,
optional real-input differential check, tolerances, and visual baselines.
The new Go implementation is MIT licensed; retained upstream and font notices
are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

Native extraction is available in the separate `extract` package.
`Extract(ctx, seeker, extract.Options{})` detects raw Annex B HEVC, Matroska,
ordinary/fragmented MP4, TS and M2TS by signature;
`DecodeT35` accepts complete registered HDR10+ payloads. The supported metadata
dialect is application ID 4, version 1, one window, with no actual-peak grids or
saturation mapping. Values, percentile indexes, target luminance and curves are
retained. Metadata inheritance follows decode order before presentation ordering.
The HEVC scope is progressive, base-layer Main/Main 10 with supported parameter
and reference syntax. Unsupported extensions return `extract.ErrUnsupportedInput`.
TrackID zero requires a unique HEVC video track; explicit IDs select the Matroska
track number, MP4 track ID, or TS PID. Raw HEVC rejects nonzero IDs.

| Container | Timing and selection |
| --- | --- |
| Matroska | All four lace modes, signed block timecodes, default/block duration, codec delay, CRC, unknown Segment/Cluster sizes; Cluster timestamp precedes its blocks |
| MP4 | Complete ordinary tables, compact sizes, signed composition offsets, supported description changes, fragment defaults/offsets and one forward media edit with optional initial empty edit |
| TS/M2TS | Bounded PAT/PMT/PES headers, CRC, original zero/33-bit timestamps, duplicate removal with permitted PCR updates, continuity checks and normal EOF |

Encryption/compression, linked Matroska segments, CodecState changes, non-unit
track scale, ambiguous sample timing, repeated/rate-changing MP4 edits and
unverified transport resets fail explicitly. Samples contain one supported
picture AU; several slices of that picture are accepted. Missing transport PTS
can permit a frame-index result; timed playlist trims require proven clocks.
Transport preflight checks mandatory markers, reserved bits, optional field
extents, clocks, rates and stuffing. DSM trick modes, embedded MPEG-1 pack headers
and embedded system headers return `extract.ErrUnsupportedInput`. PES CRC bytes
are bounded diagnostic fields; their checksum is not verified.

```go
result, err := extract.Extract(ctx, file, extract.Options{})
if err != nil {
    return err
}
metadata, err := result.PlotMetadata()
if err != nil {
    return err
}
img, err := hdr10plus.Render(metadata, hdr10plus.Options{})
```

Applications own the context, reader lifetime and image output. Extraction is
synchronous, reads compressed bytes through EOF, and invokes no subprocess or
pixel decoder. Success covers identified output pictures, not universal video
integrity: a raw cut within skipped entropy data can be structurally
indistinguishable. Cancellation and reader errors preserve their identities;
absence, incomplete coverage, unsupported input/metadata, malformed syntax,
ambiguous tracks and resource exhaustion have separate `errors.Is` sentinels.
Results own their arrays; `PlotMetadata` validates and deep-copies them.

`extract.Limits{}` uses finite defaults: two million physical/output pictures,
512 MiB accounted retained state, 64 KiB parameter sets and slice headers,
1 MiB SEI, and at most 64 queued pictures. These quotas account owned extraction
storage rather than process RSS. File extraction and assembly reserve 2 MiB
for resolution plus 128 KiB for POC history. A shared `MemoryBudget` additionally bounds
concurrent collectors and retained results for one session. Streams are single
owner; separate streams and reservation tokens can share the budget concurrently.
`MemoryBudget.Usage` reports charged and peak bytes. Completed results remain
charged for that session. Container indexes and block scratch share the operation
quota with collected syntax and output; selected compressed bodies are streamed
or bounded by the block limit. These limits do not measure allocator overhead.

`NewStream`/`Push`/`Finish` collect immutable physical `Clip` records, borrowing
each chunk only until `Push` returns. `Abort` preserves the original failure.
`Assemble` accepts completed clips and exact neutral playlist occurrences, with
half-open 45-kHz trims and explicit reset/seamless connections. It replays
decode state for each occurrence, resolves required reference lookahead and
recomputes final scenes/profile. Assembly accepts no reader and performs no IO.
Unknown clocks, missing references and unprovable splices return explicit errors.
Transport `PacketRange` boundaries require observed PES starts or physical start
and EOF. A producer that strips PES headers supplies `Marker.PESHeader` for
validation and a final `SourceEnd` marker carrying the observed packet count.
These borrowed headers are not retained. A packet count does not measure IO calls.

The [development BDInfo bridge](integration/bdinfo/README.md) is a separate
GPL-compatible module using the local, unreleased issue-45 API. It is absent from
the root dependency graph. Tests cover folder/UDF ISO timelines, shared/repeated
clips, selected STC epochs, supported seamless reference cuts, complete ES delivery
and bounded failures. Release pinning and private-media storage/performance
measurements remain external validation gates. No performance overhead percentage
is claimed. See [native fixture provenance](extract/testdata/PROVENANCE.md).
