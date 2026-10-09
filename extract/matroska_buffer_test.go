package extract

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	ebml "github.com/at-wat/ebml-go"
)

type mkvIOCounter struct {
	*bytes.Reader
	reads, seeks, maxRead int
	readLimit             int
	endError              error
	errorAt               int64
	cancel                context.CancelFunc
	noProgress            bool
	seekFailAt            int64
	seekError             error
	wrongSeekPosition     bool
}

func (r *mkvIOCounter) Read(p []byte) (int, error) {
	r.reads++
	r.maxRead = max(r.maxRead, len(p))
	if r.noProgress {
		return 0, nil
	}
	if r.readLimit > 0 {
		p = p[:min(len(p), r.readLimit)]
	}
	n, err := r.Reader.Read(p)
	if r.cancel != nil {
		r.cancel()
	}
	if r.endError != nil && (r.Len() == 0 || r.errorAt > 0 && r.Size()-int64(r.Len()) >= r.errorAt) {
		err = r.endError
	}
	return n, err
}

func (r *mkvIOCounter) Seek(offset int64, whence int) (int64, error) {
	r.seeks++
	if whence == io.SeekStart && offset == r.seekFailAt {
		if r.seekError != nil {
			position, _ := r.Reader.Seek(0, io.SeekCurrent)
			return position, r.seekError
		}
		if r.wrongSeekPosition {
			return r.Reader.Seek(offset+1, whence)
		}
	}
	return r.Reader.Seek(offset, whence)
}

func bufferedMKVFixture(t testing.TB, tags int, unknown bool) []byte {
	t.Helper()
	config, sample := containerSample(t, 4)
	block := mkvTestBlock(t, 0xa3, ebml.Block{TrackNumber: 1, Keyframe: true, Data: [][]byte{sample}})
	body := ebmlCRC(ebmlUint(0xe7, 0), block)
	cluster := ebmlElement(mkvCluster, body)
	if unknown {
		cluster = append([]byte{0x1f, 0x43, 0xb6, 0x75, 0xff}, body...)
	}
	tag := ebmlElement(0x7373, ebmlElement(0x67c8, ebmlElement(0x45a3, []byte("name")), ebmlElement(0x4487, []byte("value"))))
	cluster = append(cluster, ebmlElement(0x1254c367, bytes.Repeat(tag, tags))...)
	return mkvTestFile(config, 0, cluster, nil, true, false)
}

func TestMatroskaBufferedMetadataReads(t *testing.T) {
	data := append(bytes.Repeat([]byte{0xee}, 17), bufferedMKVFixture(t, 5000, false)...)
	r := &mkvIOCounter{Reader: bytes.NewReader(data)}
	if _, err := r.Reader.Seek(17, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	e, err := Extract(t.Context(), r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.memory.release()
	if len(e.Frames) != 1 || e.Frames[0].AUOrdinal != 0 || e.Payloads[e.Frames[0].PayloadIndex].profile() != "A" || compareTime(e.Frames[0].PTS, Timestamp{Value: 0, Timescale: 1, Valid: true}) != 0 {
		t.Fatalf("extraction changed: %+v", e)
	}
	position, _ := r.Reader.Seek(0, io.SeekCurrent)
	if position != int64(len(data)) {
		t.Fatalf("final reader position %d, want %d", position, len(data))
	}
	t.Logf("%d bytes: %d source reads, %d source seeks, maximum read request %d", len(data), r.reads, r.seeks, r.maxRead)
	if r.reads > 128 || r.seeks > 128 || r.maxRead > 32768 {
		t.Fatal("metadata traversal uses unbounded or per-element source IO")
	}
}

func TestMatroskaBufferedReadOutcomes(t *testing.T) {
	data := bufferedMKVFixture(t, 0, false)
	fault := errors.New("source read failure")
	for _, tc := range []struct {
		name  string
		limit int
		end   error
		want  error
	}{
		{"partial-reads", 3, nil, nil},
		{"bytes-and-EOF", 0, io.EOF, nil},
		{"bytes-and-source-error", 0, fault, fault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &mkvIOCounter{Reader: bytes.NewReader(data), readLimit: tc.limit, endError: tc.end}
			e, err := Extract(t.Context(), r, Options{Format: Matroska})
			if tc.want != nil {
				if e != nil || !errors.Is(err, tc.want) {
					t.Fatalf("result=%v error=%v", e, err)
				}
			} else {
				if err != nil || len(e.Frames) != 1 {
					t.Fatalf("result=%v error=%v", e, err)
				}
				e.memory.release()
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := &mkvIOCounter{Reader: bytes.NewReader(data), cancel: cancel}
	if e, err := Extract(ctx, r, Options{Format: Matroska}); e != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: result=%v error=%v", e, err)
	} else {
		detail, ok := errors.AsType[*Error](err)
		position, _ := r.Reader.Seek(0, io.SeekCurrent)
		if !ok || !detail.OffsetKnown || uint64(position) != detail.Offset || r.reads != 1 {
			t.Fatalf("logical error position/cancellation changed: position=%d error=%+v reads=%d", position, detail, r.reads)
		}
	}
}

func TestMatroskaReadBufferQuota(t *testing.T) {
	budget, err := NewMemoryBudget(32768 + 4096 + 128)
	if err != nil {
		t.Fatal(err)
	}
	r := &mkvIOCounter{Reader: bytes.NewReader(bufferedMKVFixture(t, 0, false))}
	if e, err := Extract(t.Context(), r, Options{Format: Matroska, Budget: budget}); e != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("result=%v error=%v", e, err)
	}
	used, peak := budget.Usage()
	if r.reads != 0 || r.seeks != 0 || used != 0 || peak != 0 {
		t.Fatalf("unreserved read buffers: reads=%d seeks=%d used=%d peak=%d", r.reads, r.seeks, used, peak)
	}
}

func TestMatroskaBufferedSeekFailures(t *testing.T) {
	base := bufferedMKVFixture(t, 0, false)
	segment := bytes.Index(base, []byte{0x18, 0x53, 0x80, 0x67})
	void := ebmlElement(0xec, make([]byte, 80000))
	data := bytes.Join([][]byte{base[:segment], void, base[segment:]}, nil)
	fault := errors.New("source seek failure")
	for _, tc := range []struct {
		fault error
		wrong bool
		want  error
	}{{}, {fault: fault, want: fault}, {wrong: true, want: io.ErrUnexpectedEOF}} {
		r := &mkvIOCounter{Reader: bytes.NewReader(data), seekFailAt: int64(segment + len(void)), seekError: tc.fault, wrongSeekPosition: tc.wrong}
		e, err := Extract(t.Context(), r, Options{Format: Matroska})
		if tc.want == nil {
			if err != nil || len(e.Frames) != 1 {
				t.Fatalf("seek outside cache changed extraction: result=%v error=%v", e, err)
			}
			e.memory.release()
		} else if e != nil || !errors.Is(err, tc.want) {
			t.Fatalf("wrongPosition=%v result=%v error=%v", tc.wrong, e, err)
		}
	}
	readFault := errors.New("source read failure before restoration")
	infoBody := ebmlCRC(ebmlUint(0x2ad7b1, 1))
	info := ebmlElement(mkvInfo, infoBody)
	infoStart := bytes.Index(base, info)
	r := &mkvIOCounter{Reader: bytes.NewReader(base), endError: readFault, errorAt: int64(infoStart + len(info)), seekFailAt: int64(infoStart + len(info) - len(infoBody) + 1), seekError: fault}
	if e, err := Extract(t.Context(), r, Options{Format: Matroska}); e != nil || !errors.Is(err, readFault) || !errors.Is(err, fault) {
		t.Fatalf("restoration masked an error: result=%v error=%v", e, err)
	}
	r = &mkvIOCounter{Reader: bytes.NewReader(base), noProgress: true}
	if e, err := Extract(t.Context(), r, Options{Format: Matroska}); e != nil || !errors.Is(err, io.ErrNoProgress) || r.reads != 1 {
		t.Fatalf("no-progress source retried: result=%v error=%v reads=%d", e, err, r.reads)
	}
}

func TestMatroskaBufferedFilePosition(t *testing.T) {
	data := bufferedMKVFixture(t, 5000, false)
	path := filepath.Join(t.TempDir(), "metadata.mkv")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	e, err := Extract(t.Context(), f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.memory.release()
	position, err := f.Seek(0, io.SeekCurrent)
	if err != nil || position != int64(len(data)) || len(e.Frames) != 1 {
		t.Fatalf("file position=%d error=%v frames=%d", position, err, len(e.Frames))
	}
}

func TestMatroskaBufferedSelectedBytesOnce(t *testing.T) {
	_, sample := containerSample(t, 4)
	for _, unknown := range []bool{false, true} {
		data := bufferedMKVFixture(t, 5000, unknown)
		start := bytes.Index(data, sample)
		r := &sampleReadCounter{Reader: bytes.NewReader(data), start: start, end: start + len(sample)}
		e, err := Extract(t.Context(), r, Options{})
		if err != nil {
			t.Fatal(err)
		}
		e.memory.release()
		if r.bytes != len(sample) {
			t.Errorf("unknownCluster=%v: selected bytes read %d, want %d bytes exactly once", unknown, r.bytes, len(sample))
		}
	}
}

func TestMatroskaDeferredClusterOrderedFlag(t *testing.T) {
	config, sample := containerSample(t, 4)
	for _, placement := range []struct {
		name    string
		group   bool
		unknown bool
	}{
		{"known-cluster", false, false},
		{"known-block-group", true, false},
		{"unknown-cluster", false, true},
		{"unknown-block-group", true, true},
	} {
		for _, flag := range []struct {
			name string
			body []byte
			want error
		}{
			{"zero", []byte{0}, nil},
			{"ordered", []byte{1}, ErrUnsupportedInput},
			{"oversized", make([]byte, 9), ErrInvalidBitstream},
		} {
			t.Run(placement.name+"/"+flag.name, func(t *testing.T) {
				ordered := ebmlElement(0x45dd, flag.body)
				id := uint32(0xa3)
				if placement.group {
					id = 0xa1
				}
				block := mkvTestBlock(t, id, ebml.Block{TrackNumber: 1, Keyframe: !placement.group, Data: [][]byte{sample}})
				children := bytes.Join([][]byte{ordered, block}, nil)
				if placement.group {
					children = ebmlElement(0xa0, children)
				}
				body := ebmlCRC(ebmlUint(0xe7, 0), children)
				cluster := ebmlElement(mkvCluster, body)
				if placement.unknown {
					cluster = append([]byte{0x1f, 0x43, 0xb6, 0x75, 0xff}, body...)
				}
				data := mkvTestFile(config, 0, cluster, nil, true, false)
				e, err := Extract(t.Context(), bytes.NewReader(data), Options{Format: Matroska})
				if e != nil {
					defer e.memory.release()
				}
				if flag.want != nil {
					if e != nil || !errors.Is(err, flag.want) {
						t.Fatalf("result=%v error=%v, want %v", e, err, flag.want)
					}
				} else if err != nil || e == nil || len(e.Frames) != 1 {
					t.Fatalf("zero flag changed extraction: result=%v error=%v", e, err)
				}
			})
		}
	}
}
