# Native syntax fixtures

The three `.t35` files and six `.hevc` files are newly authored synthetic
syntax fixtures, MIT licensed with this repository. They contain header syntax
and arbitrary skipped VCL bytes; they are not decodable video or a test of
compressed-picture integrity. Normal tests require no downloads or executables.

Payload values were authored independently: RGB MaxSCL `[0,100000,45678]`,
average `12345`, fraction `1023`, and the exact nine/ten supported percentile
indexes. Their values are `[0,100,200,300,400,500,600,700,99999]` or
`[0,100,200,300,400,500,600,700,800,99999]`. A has target zero and no curve;
B has target 10000, knee `[4095,0]`, anchors `[0,512,1023]`; N/A has that
target/knee with an explicitly empty anchor list. Tests assert every field.

The native headers use Main 10 base-layer syntax, sparse VPS/SPS/PPS IDs
2/3/5, explicit output flags and a reorder limit of two. `single.hevc` contains
one A picture. `reordered.hevc` decodes AU/POC `0/0, 1/2, 2/1`: AU 0 updates A,
AU 1 omits metadata and inherits A, AU 2 updates B. Display output is AU
`[0,2,1]` and payload profile `[A,B,A]`. `no-metadata.hevc` has the same
picture ordering without registered updates. Three-byte start codes and legal
emulation prevention are generated according to H.265 (01/2026) syntax.

Optional local native parity uses quietvoid/hdr10plus_tool source revision
`82cb01ba387184476dbf5fa3bddebf0a345693d7`, tool 1.7.2/library 2.1.5:
`regular.hevc` versus `regular_metadata.json` (259/A/3), and
`dhdr10-opt.hevc` versus `metadata-dhdr10-opt.json` (30/A/6). Every projected
integer, distribution order, frame count, profile and scene count is compared.
Those upstream raw assets are not redistributed here. The upstream single-frame
asset uses a profile outside this implementation's Main/Main 10 scope and is
excluded from the success parity set; the authored B fixture covers B decoding.

```powershell
$env:HDR10PLUS_REFERENCE_DIR = 'C:\path\to\hdr10plus_tool\assets\hevc_tests'
go test ./extract -run '^TestNativeUpstreamDifferential$' -v
```

The bridge's generated disc transports these same authored bytes and timestamps
through the public local BDInfo API. Tests check exact AU identities and repeats,
not merely a presence flag. `generate.py` regenerates the HEVC syntax offline;
normal tests never invoke it. `seam-profile-b.hevc` and `seam-profile-na.hevc`
decode POC 0, 2, 4 with explicit retained references. `seam-leading.hevc` is
POC 1 and uses POC 0 and 2. A trim selecting only predecessor POC 0 therefore
requires decode lookahead through POC 2, but excludes the POC 4 update. Repeated
destination occurrences inherit B or N/A according to their actual predecessor.

Go fixture constructors independently assert signed POC/wrap, CRA/RASL/RADL,
dependent/independent multislices, non-output updates and prior-output suppression.
Container constructors encode Matroska lace/CRC/unknown-size cases, MP4 tables,
fragments/edits and TS/M2TS packets/PES/PSI. Expected frame/AU maps and exact rational
times are authored separately. Read counters verify selected MKV/MP4 sample bytes
are read once, including late metadata discovery. Bridge fixture descriptors are
GPL licensed within the nested module; the MIT core imports no disc scanner.

`conformance_test.go` authors sample-crossing NAL lengths, hvcC SEI arrays,
active VPS/SPS changes, derived luma/chroma QP boundaries, CTB sizes and explicit
long-term references at different temporal layers. Expected constraints follow
H.265 (01/2026) sections 7.4.2.4.2, 7.4.3.3, 7.4.7.1 and Annex A. A changed
definition split from its new-CVS picture across physical clips is accepted only
after assembly proves that boundary; an unresolved definition at EOF fails.

`tile_transport_conformance_test.go` independently authors uniform and explicit
tile layouts, raster addresses in tile scan NAL order, dependent slices, ignored
reserved flags and active/inactive PPS temporal IDs. Expected constraints follow
H.265 (01/2026) sections 6.5.1, 7.4.2.4.4 and 7.4.7.1. Transport cases test fixed,
private and nested optional fields against their declared adaptation and PES
header extents, with valid boundary neighbors in both TS and M2TS. Field sizes
follow ITU-T H.222.0 (10/2014), tables 2-6 and 2-21:
https://www.itu.int/rec/T-REC-H.222.0-201410-S/en

`presence_conformance_test.go` distinguishes wholly absent metadata from an
unknown selected picture when a matching update was validated in a discarded
suffix, including a non-output update. It preserves metadata inheritance before
a trim and permits a trim to omit earlier unknown pictures. Presence evidence
does not carry discarded suffix payloads into a following occurrence.

`irap_conformance_test.go` independently authors valid and forbidden associated
IRAP leading-picture types, decode/output order and current/following reference
sets, including generated unavailable references and seamless assembly. It also
compares SPS temporal nesting enabled and disabled for NAL types and references
across an intervening lower temporal layer. Expected constraints follow H.265
(01/2026) sections 7.4.2.2, 7.4.3.1, 7.4.3.2 and 8.3.2; nesting declarations
are consistent between VPS and SPS. Valid neighboring sequences retain exact
presentation order and metadata projection.
Invisible sample markers retain their separate output suppression while HEVC
header output flags still participate in associated IRAP validity checks.

`association_conformance_test.go` authors same-layer non-reference leading
pictures through short and long-term references, picture-wide source syntax,
dependent IRAP flags and actual collocated picture choices across independent
I/P/B slices. Alternative list encodings, active counts and repeated lists that
select the same picture remain valid. Raw long-term cycle limits cover 4-, 8-
and 16-bit POC widths, including the inclusive maximum for absent unused
following references and cumulative sums above the per-field maximum. Expected
constraints follow H.265 (01/2026) sections 7.4.2.2, 7.4.7.1, 8.3.2 and 8.3.4.

`history_conformance_test.go` authors long-term LSB ambiguity in the normative
previous-T0 picture/RPS/intervening-picture history after older pictures leave
the live DPB. Both explicit and SPS-sourced entries, used and unused entries,
and seamless assembly follow H.265 (01/2026) section 7.4.7.1. Explicit MSB cycles,
removal before the previous T0, generated-to-real repeated POC values, CVS/EOS
resets and the maximum 16-bit LSB have independently authored valid neighbors.

`parameter_conformance_test.go` independently authors SPS/VPS ordering,
profile and level geometry, transform/PCM and conformance/display windows,
shared SPS/PPS scaling lists, derived PPS/slice bounds and VUI restrictions.
Expected constraints follow H.265 (01/2026) sections 6.5.1, 7.4.3, 7.4.5,
7.4.7, A.3, A.4 and E.3. Legal controls cover inferred sublayer levels, level
8.5, a VPS level above its SPS, inclusive DPB thresholds, actual partial tile
edges, dependent segments within one slice, and normalized reference lists.
Nonzero spatial declarations keep one segmentation mode across a CVS; mode
changes are legal at IDR resets, with same-mode and reversed-transition controls.
Long-term-reference controls verify that previous-T0 history refreshes preserve
the CVS mode.
The older CTB16 and tile-order controls now use legal transform and minimum
profile tile dimensions; their original syntax was not valid preservation
evidence. These checks cover compressed headers, not entropy or pixels.

`playlist_multislice_test.go` independently authors complete selected AUs with
independent and dependent segments, verifying reset/repeated trims, seamless
destination replay and reference lookahead while excluding unselected updates.
`transport_retransmission_test.go` uses H.222.0 v9 (08/2023) sections 2.4.3.2,
2.4.3.4, 2.4.3.5 and Annex U.3.1 for PCR changes in consecutive duplicates,
clock-extension limits, PCR/OPCR presence and complete adaptation descriptors.
The older positive OPCR-only fixture now carries its required PCR as well.

`transport_optional_test.go` and `transport_policy_test.go` independently encode
mandatory optional-field markers, reserved bits, clock/rate bounds, complete
MPEG-2 pack bodies, TREF, program counters, P-STD fields and stuffing. Expected
constraints follow H.222.0 v9 (08/2023), tables 2-6, 2-21 and 2-39 and sections
2.4.3.5 and 2.4.3.7. DSM trick modes and embedded MPEG-1/system headers are
explicitly unsupported. Seamless-splice controls test bounded DTS syntax and
flag dependencies without claiming decoder splice conformance for non-H.262
video. The older ESCR, pack and TREF controls now encode their complete required
syntax, and positive transport padding uses 0xff stuffing.
`source_transport_test.go` and the bridge's public packet-epoch tests compare
observed PES/EOF boundaries with cuts inside a long PES, retaining independently
different metadata on neighboring epochs. Public bridge adaptation and PES tests
also check unchanged report output with the collector disabled and enabled.

SHA-256 of the checked-in binary fixtures:

- no-metadata.hevc: 6073e938e1ed5f494912fed2949a8436501d2dd5e81a212f4d88bd5090e2c76c
- profile-a.t35: fd69f61c63ad19c28db8076e3b540a304a9953979c5781a3c3102506a94d400c
- profile-b.t35: 87b46205ee6076e3e1d8ad75be1843875a702a8232b566a95b7e9707fd7b22ac
- profile-na.t35: 30c556764492a73a3e546f80f4dd8f64ff9bf9a7c511ca7835110455ecb32003
- reordered.hevc: 39712c246488be6435a63b0412c2abc33f8f1f74554d2be374c35285e083b2bb
- seam-leading.hevc: db3ae7907e21962517eb8cf83ca581d424907e7e63775e7072a741ea876f52af
- seam-profile-b.hevc: 822410d0fd4265de919390c5202966913f9ffb4e13015f50179392c294cafc05
- seam-profile-na.hevc: 81558215d3eb63456694bb6dc9f2aa4dcba3429cab799d4881bd276401c16ab6
- single.hevc: 7027aa2e1b3402edc4187176d2a9cd478af8e621e65ede13ef7fbd96b6007567
