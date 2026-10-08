package extract

import (
	"bytes"
	"encoding/binary"
	"errors"
	"slices"
	"testing"

	mp4 "github.com/abema/go-mp4"
)

func mp4Box(typ string, parts ...[]byte) []byte {
	length := 8
	for _, p := range parts {
		length += len(p)
	}
	out := make([]byte, 8)
	binary.BigEndian.PutUint32(out, uint32(length))
	copy(out[4:], typ)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
func mp4Typed(t testing.TB, b mp4.IBox) []byte {
	t.Helper()
	var out bytes.Buffer
	if _, err := mp4.Marshal(&out, b, mp4.Context{}); err != nil {
		t.Fatal(err)
	}
	return mp4Box(b.GetType().String(), out.Bytes())
}
func mp4Description(config []byte) []byte {
	header := make([]byte, 78)
	binary.BigEndian.PutUint16(header[6:8], 1)
	return mp4Box("hvc1", header, mp4Box("hvcC", config))
}
func mp4TestTrack(t testing.TB, id, scale uint32, configs [][]byte, samples [][]byte, offsets []uint64, ctts *mp4.Ctts, edits *mp4.Elst, compact, co64 bool) []byte {
	t.Helper()
	var descriptions []byte
	for _, cfg := range configs {
		descriptions = append(descriptions, mp4Description(cfg)...)
	}
	stsd := make([]byte, 8)
	binary.BigEndian.PutUint32(stsd[4:], uint32(len(configs)))
	var sizes []uint32
	for _, p := range samples {
		sizes = append(sizes, uint32(len(p)))
	}
	var sizeBox []byte
	if compact {
		data := make([]byte, 12)
		data[7] = 16
		binary.BigEndian.PutUint32(data[8:], uint32(len(samples)))
		for _, size := range sizes {
			data = append(data, byte(size>>8), byte(size))
		}
		sizeBox = mp4Box("stz2", data)
	} else {
		sizeBox = mp4Typed(t, &mp4.Stsz{SampleCount: uint32(len(samples)), EntrySize: sizes})
	}
	stsc := &mp4.Stsc{}
	if len(samples) > 0 {
		stsc.Entries = []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1}}
		if len(configs) > 1 && len(samples) > 1 {
			stsc.Entries = append(stsc.Entries, mp4.StscEntry{FirstChunk: 2, SamplesPerChunk: 1, SampleDescriptionIndex: 2})
		}
	}
	stsc.EntryCount = uint32(len(stsc.Entries))
	stts := &mp4.Stts{}
	if len(samples) > 0 {
		stts.EntryCount = 1
		stts.Entries = []mp4.SttsEntry{{SampleCount: uint32(len(samples)), SampleDelta: 3000}}
	}
	var chunks []byte
	if co64 {
		chunks = mp4Typed(t, &mp4.Co64{EntryCount: uint32(len(offsets)), ChunkOffset: offsets})
	} else {
		values := make([]uint32, len(offsets))
		for i, v := range offsets {
			values[i] = uint32(v)
		}
		chunks = mp4Typed(t, &mp4.Stco{EntryCount: uint32(len(values)), ChunkOffset: values})
	}
	tables := [][]byte{mp4Box("stsd", stsd, descriptions), sizeBox, mp4Typed(t, stsc), mp4Typed(t, stts), chunks}
	if ctts != nil {
		tables = append(tables, mp4Typed(t, ctts))
	}
	handler := &mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}}
	mdia := mp4Box("mdia", mp4Typed(t, &mp4.Mdhd{Timescale: scale}), mp4Typed(t, handler), mp4Box("minf", mp4Box("stbl", tables...)))
	parts := [][]byte{mp4Typed(t, &mp4.Tkhd{TrackID: id}), mdia}
	if edits != nil {
		parts = append(parts, mp4Box("edts", mp4Typed(t, edits)))
	}
	return mp4Box("trak", parts...)
}
func mp4ReorderedSamples(t testing.TB, length int) [][]byte {
	t.Helper()
	raw := rawFixture(t, "reordered")
	var frames [][]byte
	var frame []byte
	for pos := 0; pos < len(raw); {
		end := len(raw)
		next := bytes.Index(raw[pos+3:], []byte{0, 0, 1})
		if next >= 0 {
			end = pos + 3 + next
		}
		nal := raw[pos+3 : end]
		kind := nal[0] >> 1 & 63
		if kind <= 31 || kind == 39 {
			var size [4]byte
			binary.BigEndian.PutUint32(size[:], uint32(len(nal)))
			frame = append(frame, size[4-length:]...)
			frame = append(frame, nal...)
			if kind <= 31 {
				frames = append(frames, frame)
				frame = nil
			}
		}
		pos = end
	}
	return frames
}
func mp4OrdinaryFile(t testing.TB, late, co64, compact bool) ([]byte, [][]byte) {
	t.Helper()
	config, _ := containerSample(t, 4)
	samples := mp4ReorderedSamples(t, 4)
	ftyp := mp4Box("ftyp", []byte("isom\x00\x00\x00\x00isomiso6"))
	ctts := &mp4.Ctts{FullBox: mp4.FullBox{Version: 1}, EntryCount: 3, Entries: []mp4.CttsEntry{{SampleCount: 1}, {SampleCount: 1, SampleOffsetV1: 3000}, {SampleCount: 1, SampleOffsetV1: -3000}}}
	makeMoov := func(offsets []uint64) []byte {
		return mp4Box("moov", mp4Typed(t, &mp4.Mvhd{Timescale: 1000}), mp4TestTrack(t, 1, 90000, [][]byte{config}, samples, offsets, ctts, nil, compact, co64))
	}
	moov := makeMoov([]uint64{0, 0, 0})
	position := uint64(len(ftyp) + 8)
	if !late {
		position += uint64(len(moov))
	}
	offsets := make([]uint64, len(samples))
	for i, s := range samples {
		offsets[i] = position
		position += uint64(len(s))
	}
	moov = makeMoov(offsets)
	mdat := mp4Box("mdat", samples...)
	if late {
		return bytes.Join([][]byte{ftyp, mdat, moov}, nil), samples
	}
	return bytes.Join([][]byte{ftyp, moov, mdat}, nil), samples
}
func TestMP4OrdinaryFullTimeline(t *testing.T) {
	for _, late := range []bool{false, true} {
		for _, co64 := range []bool{false, true} {
			for _, compact := range []bool{false, true} {
				data, samples := mp4OrdinaryFile(t, late, co64, compact)
				r := &sampleReadCounter{Reader: bytes.NewReader(data), start: bytes.Index(data, samples[0]), end: bytes.Index(data, samples[0]) + len(samples[0]) + len(samples[1]) + len(samples[2])}
				e, err := Extract(t.Context(), r, Options{})
				if err != nil {
					t.Fatalf("late%v co64%v compact%v: %v", late, co64, compact, err)
				}
				if len(e.Frames) != 3 {
					t.Fatal("timeline count")
				}
				for i, au := range []uint64{0, 2, 1} {
					if e.Frames[i].AUOrdinal != au || compareTime(e.Frames[i].PTS, Timestamp{Value: int64(i * 3000), Timescale: 90000, Valid: true}) != 0 {
						t.Fatalf("mapping: %+v", e.Frames)
					}
				}
				if !slices.Equal([]uint32{e.Frames[0].PayloadIndex, e.Frames[1].PayloadIndex, e.Frames[2].PayloadIndex}, []uint32{0, 1, 0}) {
					t.Fatal("inheritance")
				}
				if r.bytes != r.end-r.start {
					t.Fatalf("sample reread %d", r.bytes)
				}
				e.memory.release()
			}
		}
	}
}
func TestMP4DescriptionsEditsAndLargeRational(t *testing.T) {
	config4, sample4 := containerSample(t, 4)
	config1, sample1 := containerSample(t, 1)
	samples := [][]byte{sample4, sample1, sample1}
	const movie, media = uint32(100003), uint32(300007)
	edits := &mp4.Elst{FullBox: mp4.FullBox{Version: 1}, EntryCount: 3, Entries: []mp4.ElstEntry{
		{SegmentDurationV1: 1, MediaTimeV1: -1, MediaRateInteger: 1},
		{SegmentDurationV1: 1000, MediaTimeV1: 1, MediaRateInteger: 1},
		{SegmentDurationV1: 1000, MediaTimeV1: 3000, MediaRateInteger: 1},
	}}
	moov := func(offsets []uint64) []byte {
		return mp4Box("moov", mp4Typed(t, &mp4.Mvhd{Timescale: movie}), mp4TestTrack(t, 1, media, [][]byte{config4, config1}, samples, offsets, nil, edits, false, true))
	}
	head := moov([]uint64{0, 0, 0})
	offsets := []uint64{uint64(len(head) + 8), uint64(len(head) + 8 + len(sample4)), uint64(len(head) + 8 + len(sample4) + len(sample1))}
	data := append(moov(offsets), mp4Box("mdat", samples...)...)
	if _, err := Extract(t.Context(), bytes.NewReader(data), Options{}); !errors.Is(err, ErrUnsupportedInput) {
		t.Fatalf("repeated edit accepted: %v", err)
	}
	edits.EntryCount = 2
	edits.Entries = edits.Entries[:2]
	head = moov([]uint64{0, 0, 0})
	offsets = []uint64{uint64(len(head) + 8), uint64(len(head) + 8 + len(sample4)), uint64(len(head) + 8 + len(sample4) + len(sample1))}
	data = append(moov(offsets), mp4Box("mdat", samples...)...)
	e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
	if err != nil {
		t.Fatal(err)
	}
	// The first sample establishes metadata before the media edit's start.
	if len(e.Frames) != 1 || e.Frames[0].AUOrdinal != 1 {
		t.Fatalf("edited frames %+v", e.Frames)
	}
	want, err := rationalTime(Timestamp{Value: 1, Timescale: uint64(movie), Valid: true}, Timestamp{Value: 2999, Timescale: uint64(media), Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	if compareTime(e.Frames[0].PTS, want) != 0 || e.Frames[0].PTS.Timescale <= 1<<32 {
		t.Fatalf("edit precision %+v want%+v", e.Frames[0].PTS, want)
	}
}
func mp4FragmentFile(t testing.TB, explicit, defaultRun bool) []byte {
	t.Helper()
	config, sample := containerSample(t, 4)
	moov := mp4Box("moov", mp4Typed(t, &mp4.Mvhd{Timescale: 1000}), mp4TestTrack(t, 1, 90000, [][]byte{config}, nil, nil, nil, nil, false, false), mp4Box("mvex", mp4Typed(t, &mp4.Trex{TrackID: 1, DefaultSampleDescriptionIndex: 1, DefaultSampleDuration: 3000, DefaultSampleSize: uint32(len(sample))})))
	makeMoof := func(offset int32) []byte {
		header := &mp4.Tfhd{TrackID: 1}
		header.SetFlags(mp4.TfhdDefaultBaseIsMoof)
		if explicit {
			header.SetFlags(mp4.TfhdBaseDataOffsetPresent)
			header.BaseDataOffset = uint64(len(moov))
		}
		run := &mp4.Trun{SampleCount: 3, DataOffset: offset, Entries: make([]mp4.TrunEntry, 3)}
		run.SetFlags(1)
		if !defaultRun {
			run.SetFlags(0x301)
			for i := range run.Entries {
				run.Entries[i].SampleSize = uint32(len(sample))
				run.Entries[i].SampleDuration = 3000
			}
		}
		return mp4Box("moof", mp4Typed(t, &mp4.Mfhd{SequenceNumber: 1}), mp4Box("traf", mp4Typed(t, header), mp4Typed(t, &mp4.Tfdt{}), mp4Typed(t, run)))
	}
	moof := makeMoof(0)
	moof = makeMoof(int32(len(moof) + 8))
	return bytes.Join([][]byte{moov, moof, mp4Box("mdat", sample, sample, sample)}, nil)
}
func TestMP4FragmentDefaultsAndOffsets(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, defaults := range []bool{false, true} {
			data := mp4FragmentFile(t, explicit, defaults)
			e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
			if err != nil {
				t.Fatalf("base%v defaults%v: %v", explicit, defaults, err)
			}
			if len(e.Frames) != 3 || compareTime(e.Frames[2].PTS, Timestamp{Value: 6000, Timescale: 90000, Valid: true}) != 0 {
				t.Fatalf("fragment %+v", e.Frames)
			}
			if _, err := Extract(t.Context(), bytes.NewReader(data[:len(data)-1]), Options{}); !errors.Is(err, ErrIncomplete) {
				t.Fatalf("fragment truncation: %v", err)
			}
		}
	}
}
func TestMP4MalformedCountsAndExtents(t *testing.T) {
	data, _ := mp4OrdinaryFile(t, false, false, false)
	for _, typ := range []string{"stts", "ctts", "stsc", "stsz"} {
		bad := slices.Clone(data)
		pos := bytes.Index(bad, []byte(typ)) + 8
		if typ == "stsz" {
			pos += 4
		}
		binary.BigEndian.PutUint32(bad[pos:], 0xffffffff)
		if e, err := Extract(t.Context(), bytes.NewReader(bad), Options{}); e != nil || !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("%s count: %v %v", typ, e, err)
		}
	}
	bad := slices.Clone(data)
	pos := bytes.Index(bad, []byte("stco")) + 12
	binary.BigEndian.PutUint32(bad[pos:], 1)
	if e, err := Extract(t.Context(), bytes.NewReader(bad), Options{}); e != nil || !errors.Is(err, ErrInvalidBitstream) {
		t.Fatalf("extent: %v %v", e, err)
	}
}
