package extract

import (
	"bytes"
	"errors"
	"io"
	"testing"

	ebml "github.com/at-wat/ebml-go"
)

type mkvDiscoveryCounter struct {
	*mkvIOCounter
	starts, discoveryReads, discoverySeeks int
	discoveryEnd                           int64
}

func (r *mkvDiscoveryCounter) Read(p []byte) (int, error) {
	n, err := r.mkvIOCounter.Read(p)
	if r.starts == 1 {
		r.discoveryReads++
		r.discoveryEnd = max(r.discoveryEnd, r.Size()-int64(r.Len()))
	}
	return n, err
}

func (r *mkvDiscoveryCounter) Seek(offset int64, whence int) (int64, error) {
	if whence == io.SeekStart && offset == 0 {
		r.starts++
	} else if r.starts == 1 {
		r.discoverySeeks++
	}
	return r.mkvIOCounter.Seek(offset, whence)
}

func TestMatroskaDiscoveryStopsAtMetadata(t *testing.T) {
	config, sample := containerSample(t, 4)
	block := mkvTestBlock(t, 0xa3, ebml.Block{TrackNumber: 1, Keyframe: true, Data: [][]byte{sample}})
	first := ebmlElement(mkvCluster, ebmlUint(0xe7, 0), block)
	empty := ebmlElement(mkvCluster, ebmlUint(0xe7, 1), ebmlElement(0xec, make([]byte, 4096)))
	clusters := append(first, bytes.Repeat(empty, 1000)...)
	head := ebmlElement(mkvHeader, ebmlElement(0x4282, []byte("matroska")))
	info := ebmlElement(mkvInfo, ebmlCRC(ebmlUint(0x2ad7b1, 1)))
	track := ebmlElement(0xae, ebmlUint(0xd7, 1), ebmlUint(0x83, 1), ebmlElement(0x86, []byte("V_MPEGH/ISO/HEVC")), ebmlElement(0x63a2, config))
	tracks := ebmlElement(mkvTracks, track)
	unknown := append([]byte{0x18, 0x53, 0x80, 0x67, 0xff}, ebmlCRC(tracks, info, clusters)...)
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"info-first", mkvTestFile(config, 0, clusters, nil, false, false)},
		{"tracks-first-unknown-segment", append(head, unknown...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clusterStart := int64(bytes.Index(tc.data, first))
			r := &mkvDiscoveryCounter{mkvIOCounter: &mkvIOCounter{Reader: bytes.NewReader(tc.data)}}
			e, err := Extract(t.Context(), r, Options{Format: Matroska})
			if err != nil {
				t.Fatal(err)
			}
			defer e.memory.release()
			if len(e.Frames) != 1 || e.Frames[0].AUOrdinal != 0 || e.Payloads[e.Frames[0].PayloadIndex].profile() != "A" || compareTime(e.Frames[0].PTS, Timestamp{Value: 0, Timescale: 1, Valid: true}) != 0 || r.Len() != 0 {
				t.Fatalf("incomplete extraction: frames=%v unread=%d", e.Frames, r.Len())
			}
			t.Logf("%d bytes, 1001 clusters: discovery reads=%d seeks=%d last read end=%d, first cluster=%d", len(tc.data), r.discoveryReads, r.discoverySeeks, r.discoveryEnd, clusterStart)
			if r.starts != 2 || r.discoveryEnd > clusterStart || r.discoverySeeks > 4 {
				t.Fatal("discovery traversed clusters after Info and Tracks were available")
			}
		})
	}
}

func TestMatroskaDiscoveryValidatesTail(t *testing.T) {
	config, sample := containerSample(t, 4)
	block := mkvTestBlock(t, 0xa3, ebml.Block{TrackNumber: 1, Keyframe: true, Data: [][]byte{sample}})
	cluster := ebmlElement(mkvCluster, ebmlUint(0xe7, 0), block)
	valid := mkvTestFile(config, 0, cluster, nil, false, false)
	info := ebmlElement(mkvInfo, ebmlUint(0x2ad7b1, 1))
	tracks := ebmlElement(mkvTracks)
	chapters := ebmlElement(0x1043a770, ebmlElement(0x45b9, ebmlUint(0x45dd, 1)))
	badCluster := ebmlElement(mkvCluster, ebmlCRC(ebmlUint(0xe7, 1)))
	badCluster[len(badCluster)-1] ^= 1
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"duplicate-info", mkvTestFile(config, 0, append(bytes.Clone(cluster), info...), nil, false, false), ErrInvalidBitstream},
		{"duplicate-tracks", mkvTestFile(config, 0, append(bytes.Clone(cluster), tracks...), nil, false, false), ErrInvalidBitstream},
		{"ordered-chapters", mkvTestFile(config, 0, append(bytes.Clone(cluster), chapters...), nil, false, false), ErrUnsupportedInput},
		{"nested-segment", mkvTestFile(config, 0, append(bytes.Clone(cluster), ebmlElement(mkvSegment)...), nil, false, false), ErrUnsupportedInput},
		{"second-root", append(bytes.Clone(valid), ebmlElement(mkvSegment)...), ErrUnsupportedInput},
		{"tail-crc", mkvTestFile(config, 0, append(bytes.Clone(cluster), badCluster...), nil, false, false), ErrInvalidBitstream},
		{"truncated-tail", valid[:len(valid)-3], ErrIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			budget, err := NewMemoryBudget(512 << 20)
			if err != nil {
				t.Fatal(err)
			}
			e, err := Extract(t.Context(), bytes.NewReader(tc.data), Options{Format: Matroska, Budget: budget})
			if e != nil || !errors.Is(err, tc.want) {
				if e != nil {
					e.memory.release()
				}
				t.Fatalf("result=%v error=%v, want %v", e, err, tc.want)
			}
			if budget.used != 0 {
				t.Fatalf("failure retained %d bytes", budget.used)
			}
		})
	}
	for _, sourceError := range []error{errors.New("tail source error"), io.ErrNoProgress} {
		r := &mkvIOCounter{Reader: bytes.NewReader(valid), endError: sourceError, errorAt: int64(bytes.Index(valid, cluster))}
		if e, err := Extract(t.Context(), r, Options{Format: Matroska}); e != nil || !errors.Is(err, sourceError) {
			t.Fatalf("tail source error lost: result=%v error=%v", e, err)
		}
	}
}
