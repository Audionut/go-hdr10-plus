package extract

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	mp4 "github.com/abema/go-mp4"
	ebml "github.com/at-wat/ebml-go"
)

func sampleContainer(t testing.TB, name string, config []byte, samples [][]byte) []byte {
	t.Helper()
	if name == "MKV" || name == "MKV-lace" {
		var blocks []byte
		if name == "MKV-lace" {
			blocks = mkvTestBlock(t, 0xa3, ebml.Block{TrackNumber: 1, Lacing: ebml.LacingXiph, Data: samples})
		} else {
			for i, sample := range samples {
				blocks = append(blocks, mkvTestBlock(t, 0xa3, ebml.Block{TrackNumber: 1, Timecode: int16(i), Data: [][]byte{sample}})...)
			}
		}
		return mkvTestFile(config, 33333333, ebmlElement(mkvCluster, ebmlUint(0xe7, 0), blocks), nil, false, false)
	}
	if name == "MP4" {
		makeMoov := func(offsets []uint64) []byte {
			return mp4Box("moov", mp4Typed(t, &mp4.Mvhd{Timescale: 1000}), mp4TestTrack(t, 1, 90000, [][]byte{config}, samples, offsets, nil, nil, false, false))
		}
		offsets := make([]uint64, len(samples))
		position := uint64(len(makeMoov(offsets)) + 8)
		for i, sample := range samples {
			offsets[i] = position
			position += uint64(len(sample))
		}
		return append(makeMoov(offsets), mp4Box("mdat", samples...)...)
	}
	moov := mp4Box("moov", mp4Typed(t, &mp4.Mvhd{Timescale: 1000}), mp4TestTrack(t, 1, 90000, [][]byte{config}, nil, nil, nil, nil, false, false), mp4Box("mvex", mp4Typed(t, &mp4.Trex{TrackID: 1, DefaultSampleDescriptionIndex: 1, DefaultSampleDuration: 3000})))
	makeMoof := func(offset int32) []byte {
		header := &mp4.Tfhd{TrackID: 1}
		header.SetFlags(mp4.TfhdDefaultBaseIsMoof)
		run := &mp4.Trun{SampleCount: uint32(len(samples)), DataOffset: offset, Entries: make([]mp4.TrunEntry, len(samples))}
		run.SetFlags(0x201)
		for i, sample := range samples {
			run.Entries[i].SampleSize = uint32(len(sample))
		}
		return mp4Box("moof", mp4Typed(t, &mp4.Mfhd{SequenceNumber: 1}), mp4Box("traf", mp4Typed(t, header), mp4Typed(t, &mp4.Tfdt{}), mp4Typed(t, run)))
	}
	return bytes.Join([][]byte{moov, makeMoof(int32(len(makeMoof(0)) + 8)), mp4Box("mdat", samples...)}, nil)
}

func TestContainerNALSampleBoundaries(t *testing.T) {
	for length := 1; length <= 4; length++ {
		config, sample := containerSample(t, length)
		var encoded [4]byte
		copy(encoded[4-length:], sample[:length])
		lastPrefix := length + int(binary.BigEndian.Uint32(encoded[:]))
		for _, container := range []string{"MP4", "fragmented-MP4", "MKV", "MKV-lace"} {
			t.Run(fmt.Sprintf("%s/length%d", container, length), func(t *testing.T) {
				clean := sampleContainer(t, container, config, [][]byte{sample, sample})
				if e, err := Extract(t.Context(), bytes.NewReader(clean), Options{}); err != nil || len(e.Frames) != 2 {
					t.Fatalf("valid samples: %v %v", e, err)
				}
				cuts := []int{len(sample) - 2}
				if length > 1 {
					cuts = append(cuts, lastPrefix+length-1)
				}
				for _, cut := range cuts {
					samples := [][]byte{sample[:cut], append(slices.Clone(sample[cut:]), sample...)}
					data := sampleContainer(t, container, config, samples)
					if e, err := Extract(t.Context(), bytes.NewReader(data), Options{}); e != nil || !errors.Is(err, ErrInvalidBitstream) || !errors.Is(err, ErrIncomplete) {
						t.Fatalf("cut%d: %v %v", cut, e, err)
					}
				}
			})
		}
	}
}

func TestInteriorSampleMarkerAndConfigurationBoundary(t *testing.T) {
	config, sample := containerSample(t, 4)
	for _, split := range []bool{false, true} {
		s, err := NewStream(StreamOptions{Framing: LengthPrefixed, LengthSize: 4, CodecConfig: config})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.memory.release)
		data := append(slices.Clone(sample), sample...)
		markers := []Marker{{Kind: SampleStart}, {Kind: SampleStart, ESOffset: uint64(len(sample))}}
		if split {
			// A chunk split inside a NAL is valid when it is not a sample start.
			if err := s.Push(t.Context(), Chunk{Data: data[:len(sample)-2], Markers: markers[:1]}); err != nil {
				t.Fatal(err)
			}
			if err := s.Push(t.Context(), Chunk{Data: data[len(sample)-2:], ESOffset: uint64(len(sample) - 2), Markers: markers[1:]}); err != nil {
				t.Fatal(err)
			}
		} else if err := s.Push(t.Context(), Chunk{Data: data, Markers: markers}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Finish(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	for _, configuration := range []bool{false, true} {
		s, err := NewStream(StreamOptions{Framing: LengthPrefixed, LengthSize: 4, CodecConfig: config})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.memory.release)
		var boundaryErr error
		if configuration {
			if err := s.Push(t.Context(), Chunk{Data: sample[:len(sample)-2]}); err != nil {
				t.Fatal(err)
			}
			boundaryErr = s.config(config)
		} else {
			data := append(slices.Clone(sample), sample...)
			boundaryErr = s.Push(t.Context(), Chunk{Data: data, Markers: []Marker{{Kind: SampleStart}, {Kind: SampleStart, ESOffset: uint64(len(sample) - 2)}}})
		}
		if !errors.Is(boundaryErr, ErrInvalidBitstream) || !errors.Is(boundaryErr, ErrIncomplete) {
			t.Fatalf("configuration%v: %v", configuration, boundaryErr)
		}
	}
}

func appendConfigNAL(config []byte, kind byte, nal []byte) []byte {
	config = slices.Clone(config)
	config[22]++
	return append(append(config, kind, 0, 1, byte(len(nal)>>8), byte(len(nal))), nal...)
}

func TestConfigurationSEIValidation(t *testing.T) {
	config, sample := containerSample(t, 4)
	bare := sample[4+int(binary.BigEndian.Uint32(sample[:4])):]
	_, metadata, _ := headerFixtureParts(t)
	prefix := slices.Clone(metadata[3:])
	unsupported := slices.Clone(prefix)
	unsupported[10] = 0 // NAL header2 + SEI header2 + identity6 precede version.
	suffix := slices.Clone(prefix)
	suffix[0] = 40 << 1
	// This envelope is larger than the PS quota, but fits its own SEI quota.
	unrelated := authoredNAL(39, append(append([]byte{5, 255, 255, 190}, bytes.Repeat([]byte{0xff}, 700)...), 0x80))[3:]
	for _, container := range []string{"MP4", "MKV"} {
		for _, tc := range []struct {
			name        string
			kind        byte
			nal, sample []byte
			limits      Limits
			want        error
		}{
			{"prefix-valid", 39, prefix, bare, Limits{}, nil},
			{"unsupported-with-HDR", 39, unsupported, sample, Limits{}, ErrUnsupportedMetadata},
			{"unsupported-without-HDR", 39, unsupported, bare, Limits{}, ErrUnsupportedMetadata},
			{"suffix", 40, suffix, sample, Limits{}, ErrUnsupportedInput},
			{"unrelated-own-quota", 39, unrelated, sample, Limits{MaxParameterSetBytes: 128, MaxSEIBytes: 1024}, nil},
			{"SEI-over-quota", 39, unrelated, sample, Limits{MaxSEIBytes: 512}, ErrResourceLimit},
		} {
			t.Run(container+"/"+tc.name, func(t *testing.T) {
				data := sampleContainer(t, container, appendConfigNAL(config, tc.kind, tc.nal), [][]byte{tc.sample})
				e, err := Extract(t.Context(), bytes.NewReader(data), Options{Limits: tc.limits})
				if tc.want != nil {
					if e != nil || !errors.Is(err, tc.want) || errors.Is(err, ErrNoMetadata) {
						t.Fatalf("got %v %v want %v", e, err, tc.want)
					}
				} else if err != nil || len(e.Frames) != 1 {
					t.Fatalf("valid configuration: %v %v", e, err)
				}
			})
		}
	}
}

func seBits(v int64) string {
	if v > 0 {
		return ueBits(uint64(2*v - 1))
	}
	return ueBits(uint64(-2 * v))
}

func main10PTL(level uint8) string {
	return "000" + fmt.Sprintf("%05b%032b", 2, 1<<29) + "1001" + strings.Repeat("0", 44) + fmt.Sprintf("%08b", level)
}

func conformanceSPS(id, width, depth, ctb uint64) string {
	return "00100001" + main10PTL(120) + ueBits(id) + ueBits(1) + ueBits(width) + ueBits(64) + "0" + ueBits(depth) + ueBits(depth) + ueBits(4) + "1" + ueBits(3) + ueBits(2) + ueBits(0) + ueBits(0) + ueBits(ctb-3) + ueBits(0) + ueBits(min(ctb, 5)-2) + ueBits(0) + ueBits(0) + "0000" + ueBits(0) + "00000"
}

func conformancePPS(init, cb, cr int64, offsets bool) string {
	flag := "0"
	if offsets {
		flag = "1"
	}
	return ueBits(5) + ueBits(3) + "1100000" + ueBits(0) + ueBits(0) + seBits(init) + "000" + seBits(cb) + seBits(cr) + flag + "000000000" + ueBits(0) + "00"
}

func TestDerivedHeaderConstraints(t *testing.T) {
	sets, metadata, _ := headerFixtureParts(t)
	vps := sets[:bytes.Index(sets, []byte{0, 0, 1, 66, 1})]
	for _, tc := range []struct {
		name                            string
		depth, ctb                      uint64
		init, delta, pCb, pCr, sCb, sCr int64
		offsets, valid                  bool
	}{
		{name: "CTB8", depth: 2, ctb: 3},
		{name: "CTB16", depth: 2, ctb: 4, valid: true},
		{name: "QP52", depth: 2, ctb: 6, delta: 26},
		{name: "QP51", depth: 2, ctb: 6, delta: 25, valid: true},
		{name: "QP-minus13", depth: 2, ctb: 6, delta: -39},
		{name: "QP-minus12", depth: 2, ctb: 6, delta: -38, valid: true},
		{name: "8bit-init-minus27-final0", ctb: 6, init: -27, delta: 1},
		{name: "8bit-init-minus26", ctb: 6, init: -26, valid: true},
		{name: "10bit-init-minus38", depth: 2, ctb: 6, init: -38, valid: true},
		{name: "Cb-plus13", depth: 2, ctb: 6, pCb: 12, sCb: 1, offsets: true},
		{name: "Cb-plus12", depth: 2, ctb: 6, pCb: 11, sCb: 1, offsets: true, valid: true},
		{name: "Cb-minus13", depth: 2, ctb: 6, pCb: -12, sCb: -1, offsets: true},
		{name: "Cb-minus12", depth: 2, ctb: 6, pCb: -11, sCb: -1, offsets: true, valid: true},
		{name: "Cr-plus13", depth: 2, ctb: 6, pCr: 12, sCr: 1, offsets: true},
		{name: "Cr-plus12", depth: 2, ctb: 6, pCr: 11, sCr: 1, offsets: true, valid: true},
		{name: "Cr-minus13", depth: 2, ctb: 6, pCr: -12, sCr: -1, offsets: true},
		{name: "Cr-minus12", depth: 2, ctb: 6, pCr: -11, sCr: -1, offsets: true, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := append(slices.Clone(vps), authoredNAL(33, packedHeader(conformanceSPS(3, 64, tc.depth, tc.ctb)+"1"))...)
			data = append(data, authoredNAL(34, packedHeader(conformancePPS(tc.init, tc.pCb, tc.pCr, tc.offsets)+"1"))...)
			data = append(data, metadata...)
			bits := "11" + ueBits(5) + ueBits(2) + "1" + seBits(tc.delta)
			if tc.offsets {
				bits += seBits(tc.sCb) + seBits(tc.sCr)
			}
			data = append(data, authoredNAL(19, append(packedHeader(bits+"1"), 0xff, 0xff))...)
			e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
			if tc.valid {
				if err != nil || len(e.Frames) != 1 {
					t.Fatalf("valid boundary: %v %v", e, err)
				}
			} else if e != nil || !errors.Is(err, ErrInvalidBitstream) {
				t.Fatalf("invalid header: %v %v", e, err)
			}
		})
	}
}

func TestActiveParameterVersions(t *testing.T) {
	raw := rawFixture(t, "single")
	sets, metadata, picture := headerFixtureParts(t)
	spsStart := bytes.Index(sets, []byte{0, 0, 1, 66, 1})
	ppsStart := bytes.Index(sets, []byte{0, 0, 1, 68, 1})
	changed := authoredNAL(33, packedHeader(conformanceSPS(3, 64, 2, 6)+"1"))
	unused := authoredNAL(33, packedHeader(conformanceSPS(4, 64, 2, 6)+"1"))
	vpsBits := "0010110000000001" + strings.Repeat("1", 16) + main10PTL(123) + "1" + ueBits(3) + ueBits(2) + ueBits(0) + "000000" + ueBits(0) + "00"
	changedVPS := authoredNAL(32, packedHeader(vpsBits+"1"))
	pps4 := strings.Replace(conformancePPS(0, 0, 0, false), ueBits(5)+ueBits(3), ueBits(5)+ueBits(4), 1)
	next := authoredReferencePicture(1, 1, 2, nil, nil)
	nextIDR := slices.Clone(picture)
	nextIDR[5] &^= 0x40
	for _, tc := range []struct {
		name   string
		tail   []byte
		want   error
		frames int
	}{
		{"changed-SPS-mid-CVS", append(slices.Clone(changed), next...), ErrInvalidBitstream, 0},
		{"changed-VPS-mid-CVS", append(slices.Clone(changedVPS), next...), ErrInvalidBitstream, 0},
		{"SPS-ID-switch", bytes.Join([][]byte{unused, authoredNAL(34, packedHeader(pps4+"1")), next}, nil), ErrInvalidBitstream, 0},
		{"identical-SPS", append(slices.Clone(sets[spsStart:ppsStart]), next...), nil, 2},
		{"inactive-SPS", append(slices.Clone(unused), next...), nil, 2},
		{"PPS-between-pictures", append(authoredNAL(34, packedHeader(conformancePPS(1, 0, 0, false)+"1")), next...), nil, 2},
		{"changed-SPS-before-IDR", bytes.Join([][]byte{changed, metadata, nextIDR}, nil), nil, 2},
		{"changed-SPS-before-CRA-in-CVS", append(slices.Clone(changed), authoredReferencePicture(1, 21, 2, nil, nil)...), ErrInvalidBitstream, 0},
		{"changed-SPS-EOF", changed, ErrIncomplete, 0},
		{"changed-VPS-EOF", changedVPS, ErrIncomplete, 0},
		{"inactive-SPS-EOF", unused, nil, 1},
		{"changed-then-restored-SPS", bytes.Join([][]byte{changed, sets[spsStart:ppsStart], next}, nil), ErrInvalidBitstream, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := append(slices.Clone(raw), tc.tail...)
			e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
			if tc.want != nil {
				if e != nil || !errors.Is(err, tc.want) {
					t.Fatalf("got %v %v want %v", e, err, tc.want)
				}
			} else if err != nil || len(e.Frames) != tc.frames {
				t.Fatalf("valid version: %v %v", e, err)
			}
		})
	}
	// A definition at physical EOF is pending boundary syntax, not standalone
	// success. Assembly proves the new CVS in the following physical occurrence.
	aKey, bKey := StreamKey{SourceID: "parameter-before-CVS"}, StreamKey{SourceID: "new-CVS"}
	a := sourceClip(t, aKey, append(slices.Clone(raw), changed...), []int64{0})
	b := sourceClip(t, bKey, append(slices.Clone(metadata), nextIDR...), []int64{0})
	occurrences := []Occurrence{{Key: aKey, Out: 1500, ClockOffset: Timestamp{Timescale: 90000, Valid: true}, Connection: ConnectionReset}, {Key: bKey, Out: 1500, PlaylistStart: 1500, ClockOffset: Timestamp{Timescale: 90000, Valid: true}, Connection: ConnectionSeamless}}
	e, err := Assemble(t.Context(), Playlist{Occurrences: occurrences}, map[StreamKey]*Clip{aKey: a, bKey: b}, AssemblyOptions{})
	if err != nil || len(e.Frames) != 2 || e.Frames[0].Stream != aKey || e.Frames[1].Stream != bKey {
		t.Fatalf("new CVS across clips: %v %v", e, err)
	}
	if e, err := Assemble(t.Context(), Playlist{Occurrences: occurrences[:1]}, map[StreamKey]*Clip{aKey: a}, AssemblyOptions{}); e != nil || !errors.Is(err, ErrIncomplete) {
		t.Fatalf("unresolved playlist EOF: %v %v", e, err)
	}
}

func TestExplicitLongTermTemporalLimit(t *testing.T) {
	_, metadata, picture := headerFixtureParts(t)
	ptl := main10PTL(120) + strings.Repeat("0", 16)
	vps := "0010110000000011" + strings.Repeat("1", 16) + ptl + "1" + ueBits(0) + ueBits(0) + ueBits(0) + ueBits(3) + ueBits(2) + ueBits(0) + "000000" + ueBits(0) + "00"
	sps := "00100011" + ptl + ueBits(3) + ueBits(1) + ueBits(128) + ueBits(64) + "0" + ueBits(2) + ueBits(2) + ueBits(4) + "1" + ueBits(0) + ueBits(0) + ueBits(0) + ueBits(3) + ueBits(2) + ueBits(0) + ueBits(0) + ueBits(3) + ueBits(0) + ueBits(3) + ueBits(0) + ueBits(0) + "0000" + ueBits(0) + "1" + ueBits(0) + "0000"
	base := bytes.Join([][]byte{authoredNAL(32, packedHeader(vps+"1")), authoredNAL(33, packedHeader(sps+"1")), authoredNAL(34, packedHeader(conformancePPS(0, 0, 0, false)+"1")), metadata, picture}, nil)
	for _, tc := range []struct {
		name     string
		temporal byte
		count    uint64
		valid    bool
	}{{"layer0-count1", 0, 1, false}, {"layer1-count1", 1, 1, true}, {"layer0-count0", 0, 0, true}} {
		t.Run(tc.name, func(t *testing.T) {
			bits := "1" + ueBits(5) + ueBits(2) + "1" + "00000001" + "0" + ueBits(0) + ueBits(0) + ueBits(tc.count)
			if tc.count != 0 {
				bits += "00000000" + "00" // POC0, unused, no MSB-cycle delta.
			}
			bits += seBits(0) + "1"
			kind := byte(1)
			if tc.temporal > 0 {
				kind = 3 // Nested higher sublayers permit TSA, not TRAIL.
			}
			nal := authoredNAL(kind, append(packedHeader(bits), 0xff, 0xff))
			nal[4] = tc.temporal + 1
			e, err := Extract(t.Context(), bytes.NewReader(append(slices.Clone(base), nal...)), Options{})
			if tc.valid {
				if err != nil || len(e.Frames) != 2 {
					t.Fatalf("valid temporal count: %v %v", e, err)
				}
			} else if e != nil || !errors.Is(err, ErrInvalidBitstream) {
				t.Fatalf("invalid temporal count: %v %v", e, err)
			}
		})
	}
}
