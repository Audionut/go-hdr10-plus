package extract

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"slices"
	"testing"

	ebml "github.com/at-wat/ebml-go"
)

// These containers are constructed independently from their expected timings.
// The HEVC headers and T.35 values come from the authored, checked-in fixtures.
func containerSample(t testing.TB, length int) (config, sample []byte) {
	t.Helper()
	raw := rawFixture(t, "single")
	config = make([]byte, 23)
	config[0], config[21], config[22] = 1, byte(length-1), 3
	for pos := 0; pos < len(raw); {
		next := bytes.Index(raw[pos+3:], []byte{0, 0, 1})
		end := len(raw)
		if next >= 0 {
			end = pos + 3 + next
		}
		nal := slices.Clone(raw[pos+3 : end])
		kind := nal[0] >> 1 & 63
		if kind >= 32 && kind <= 34 {
			config = append(config, kind, 0, 1, byte(len(nal)>>8), byte(len(nal)))
			config = append(config, nal...)
		} else {
			if kind == 19 {
				nal[2] &^= 0x40 // do not discard output of the preceding IDR
			}
			var size [4]byte
			binary.BigEndian.PutUint32(size[:], uint32(len(nal)))
			sample = append(sample, size[4-length:]...)
			sample = append(sample, nal...)
		}
		pos = end
	}
	return config, sample
}
func ebmlElement(id uint32, body ...[]byte) []byte {
	var n int
	for _, p := range body {
		n += len(p)
	}
	var ids [4]byte
	binary.BigEndian.PutUint32(ids[:], id)
	i := 0
	for ids[i] == 0 {
		i++
	}
	width := 1
	for uint64(n) >= (uint64(1)<<(7*width))-1 {
		width++
	}
	var sizes [8]byte
	binary.BigEndian.PutUint64(sizes[:], uint64(n)|(uint64(1)<<(7*width)))
	out := append(slices.Clone(ids[i:]), sizes[8-width:]...)
	for _, p := range body {
		out = append(out, p...)
	}
	return out
}
func ebmlUint(id uint32, v uint64) []byte {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], v)
	i := 0
	for i < 7 && data[i] == 0 {
		i++
	}
	return ebmlElement(id, data[i:])
}
func ebmlCRC(body ...[]byte) []byte {
	joined := bytes.Join(body, nil)
	var crc [4]byte
	binary.LittleEndian.PutUint32(crc[:], crc32.ChecksumIEEE(joined))
	return append(ebmlElement(0xbf, crc[:]), joined...)
}
func mkvTestFile(config []byte, duration uint64, cluster, moreTracks []byte, late, unknown bool) []byte {
	head := ebmlElement(mkvHeader, ebmlElement(0x4282, []byte("matroska")))
	info := ebmlElement(mkvInfo, ebmlCRC(ebmlUint(0x2ad7b1, 1)))
	track := ebmlElement(0xae, ebmlUint(0xd7, 1), ebmlUint(0x83, 1), ebmlElement(0x86, []byte("V_MPEGH/ISO/HEVC")), ebmlElement(0x63a2, config), ebmlUint(0x23e383, duration))
	tracks := ebmlElement(mkvTracks, track, moreTracks)
	parts := [][]byte{info, tracks, cluster}
	if late {
		parts = [][]byte{cluster, info, tracks}
	}
	segment := ebmlElement(mkvSegment, ebmlCRC(parts...))
	if unknown {
		segment = append([]byte{0x18, 0x53, 0x80, 0x67, 0xff}, ebmlCRC(parts...)...)
		return bytes.Join([][]byte{head, ebmlElement(0xec, []byte{0}), segment}, nil)
	}
	return bytes.Join([][]byte{head, ebmlElement(0xec, []byte{0}), segment, ebmlElement(0xec)}, nil)
}
func mkvTestBlock(t testing.TB, id uint32, b ebml.Block) []byte {
	t.Helper()
	var data bytes.Buffer
	if err := ebml.MarshalBlock(&b, &data); err != nil {
		t.Fatal(err)
	}
	return ebmlElement(id, data.Bytes())
}

func TestMatroskaLacingAndTiming(t *testing.T) {
	for length := 1; length <= 4; length++ {
		for _, lace := range []ebml.LacingMode{ebml.LacingNo, ebml.LacingXiph, ebml.LacingFixed, ebml.LacingEBML} {
			config, sample := containerSample(t, length)
			count := 7
			if lace == ebml.LacingNo {
				count = 1
			}
			frames := make([][]byte, count)
			for i := range frames {
				frames[i] = sample
			}
			block := mkvTestBlock(t, 0xa1, ebml.Block{TrackNumber: 1, Timecode: -10, Lacing: lace, Data: frames})
			group := ebmlElement(0xa0, block, ebmlUint(0x9b, 100003))
			cluster := ebmlElement(mkvCluster, ebmlCRC(ebmlUint(0xe7, 10), group))
			e, err := Extract(t.Context(), bytes.NewReader(mkvTestFile(config, 0, cluster, nil, length%2 == 0, false)), Options{})
			if err != nil {
				t.Fatalf("length%d lace%d: %v", length, lace, err)
			}
			if len(e.Frames) != count {
				t.Fatalf("frames %d want%d", len(e.Frames), count)
			}
			for i, f := range e.Frames {
				want := Timestamp{Value: int64(i * 100003), Timescale: uint64(count) * 1000000000, Valid: true}
				if f.AUOrdinal != uint64(i) || f.Stream.TrackID != 1 || compareTime(f.PTS, want) != 0 || e.Payloads[f.PayloadIndex].profile() != "A" {
					t.Fatalf("frame%d: %+v", i, f)
				}
			}
			if count > 1 && e.Frames[1].PTS.Timescale <= 1<<32 {
				t.Fatal("lace rational narrowed")
			}
			e.memory.release()
		}
	}
}

func TestMatroskaUnknownClustersDefaultTimingAndSelection(t *testing.T) {
	config, sample := containerSample(t, 4)
	laced := mkvTestBlock(t, 0xa3, ebml.Block{TrackNumber: 1, Keyframe: true, Lacing: ebml.LacingEBML, Data: [][]byte{sample, sample}})
	clusterBody := ebmlCRC(ebmlUint(0xe7, 0), laced)
	cluster := append([]byte{0x1f, 0x43, 0xb6, 0x75, 0xff}, clusterBody...)
	// A level-one element closes the unknown-sized cluster; neither CRC hashes
	// that next header twice or includes it in the prior cluster.
	cluster = append(cluster, ebmlElement(0x1254c367, ebmlCRC(ebmlElement(0x7373)))...)
	for _, unknown := range []bool{false, true} {
		e, err := Extract(t.Context(), bytes.NewReader(mkvTestFile(config, 33333333, cluster, nil, false, unknown)), Options{})
		if err != nil {
			t.Fatalf("unknownSegment%v: %v", unknown, err)
		}
		if len(e.Frames) != 2 || compareTime(e.Frames[1].PTS, Timestamp{Value: 33333333, Timescale: 1000000000, Valid: true}) != 0 {
			t.Fatal("default duration")
		}
		e.memory.release()
	}
	other := ebmlElement(0xae, ebmlUint(0xd7, 2), ebmlUint(0x83, 1), ebmlElement(0x86, []byte("V_MPEGH/ISO/HEVC")), ebmlElement(0x63a2, config))
	data := mkvTestFile(config, 33333333, cluster, other, false, false)
	if _, err := Extract(t.Context(), bytes.NewReader(data), Options{}); !errors.Is(err, ErrAmbiguousTrack) {
		t.Fatalf("ambiguous: %v", err)
	}
	if _, err := Extract(t.Context(), bytes.NewReader(data), Options{TrackID: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestMatroskaFailuresAndFullRead(t *testing.T) {
	config, sample := containerSample(t, 4)
	validBlock := mkvTestBlock(t, 0xa3, ebml.Block{TrackNumber: 1, Keyframe: true, Data: [][]byte{sample}})
	validCluster := ebmlElement(mkvCluster, ebmlCRC(ebmlUint(0xe7, 0), validBlock))
	valid := mkvTestFile(config, 0, validCluster, nil, false, false)
	badCRC := slices.Clone(valid)
	badCRC[bytes.Index(badCRC, sample)+len(sample)-1] ^= 1
	singleLace := mkvTestBlock(t, 0xa3, ebml.Block{TrackNumber: 1, Lacing: ebml.LacingFixed, Data: [][]byte{sample}})
	untimedLace := mkvTestBlock(t, 0xa3, ebml.Block{TrackNumber: 1, Lacing: ebml.LacingEBML, Data: [][]byte{sample, sample}})
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"CRC", badCRC, ErrInvalidBitstream},
		{"truncated-final-cluster", valid[:len(valid)-3], ErrIncomplete},
		{"single-lace", mkvTestFile(config, 0, ebmlElement(mkvCluster, ebmlUint(0xe7, 0), singleLace), nil, false, false), ErrInvalidBitstream},
		{"untimed-lace", mkvTestFile(config, 0, ebmlElement(mkvCluster, ebmlUint(0xe7, 0), untimedLace), nil, false, false), ErrUnsupportedInput},
		{"state", mkvTestFile(config, 0, ebmlElement(mkvCluster, ebmlUint(0xe7, 0), ebmlElement(0xa0, mkvTestBlock(t, 0xa1, ebml.Block{TrackNumber: 1, Data: [][]byte{sample}}), ebmlElement(0xa4, []byte{1}))), nil, false, false), ErrUnsupportedInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if e, err := Extract(t.Context(), bytes.NewReader(tc.data), Options{}); e != nil || !errors.Is(err, tc.want) {
				t.Fatalf("%v %v", e, err)
			}
		})
	}
	// The adapter reads selected block bytes once despite looking up headers
	// after clusters. The bounded signature probe does not touch any sample.
	reader := &sampleReadCounter{Reader: bytes.NewReader(mkvTestFile(config, 0, validCluster, nil, true, false))}
	reader.start = bytes.Index(reader.ReaderBytes(t), sample)
	reader.end = reader.start + len(sample)
	if _, err := Extract(t.Context(), reader, Options{}); err != nil {
		t.Fatal(err)
	}
	if reader.bytes != len(sample) {
		t.Fatalf("selected bytes read%d want%d", reader.bytes, len(sample))
	}
}

type sampleReadCounter struct {
	*bytes.Reader
	start, end, bytes int
}

func (r *sampleReadCounter) Read(p []byte) (int, error) {
	position, _ := r.Seek(0, io.SeekCurrent)
	n, err := r.Reader.Read(p)
	r.bytes += max(0, min(int(position)+n, r.end)-max(int(position), r.start))
	return n, err
}
func (r *sampleReadCounter) ReaderBytes(t testing.TB) []byte {
	t.Helper()
	position, _ := r.Seek(0, io.SeekCurrent)
	data := make([]byte, r.Size())
	_, err := r.Reader.ReadAt(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = r.Seek(position, io.SeekStart)
	return data
}

func TestOperationQuotaIncludesOutputTransfer(t *testing.T) {
	b, _ := NewMemoryBudget(10000)
	parent := &accounting{max: 2056, shared: b}
	child := &accounting{max: 2056, shared: b, parent: parent}
	scratch, err := parent.reserve(800)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = child.reserve(1000); err != nil {
		t.Fatal(err)
	}
	if _, err = parent.reserve(1); !errors.Is(err, ErrResourceLimit) {
		t.Fatal("combined quota bypassed")
	}
	scratch.release()
	parent.release()
	if parent.used != 1128 || b.used != 1128 {
		t.Fatalf("transfer freed retained output: %d %d", parent.used, b.used)
	}
	child.release()
	if parent.used != 0 || b.used != 0 {
		t.Fatal("retained release")
	}
}
