package extract

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	ebml "github.com/at-wat/ebml-go"
)

func largePictureSample(t testing.TB, extra int) (config, sample []byte) {
	t.Helper()
	config, original := containerSample(t, 4)
	for pos := 0; pos < len(original); {
		size := int(binary.BigEndian.Uint32(original[pos : pos+4]))
		nal := original[pos+4 : pos+4+size]
		length := size
		if nal[0]>>1&63 <= 31 {
			length += extra
		}
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], uint32(length))
		sample = append(sample, prefix[:]...)
		sample = append(sample, nal...)
		if length != size {
			sample = append(sample, bytes.Repeat([]byte{0x55}, extra)...)
		}
		pos += size + 4
	}
	return config, sample
}

func blockStorageFixture(t testing.TB, sizes []int) []byte {
	t.Helper()
	config, _ := containerSample(t, 4)
	var blocks []byte
	for i, size := range sizes {
		_, sample := largePictureSample(t, size)
		id := uint32(0xa3)
		if i%2 != 0 {
			id = 0xa1
		}
		block := mkvTestBlock(t, id, ebml.Block{TrackNumber: 1, Timecode: int16(i*10 - 10), Invisible: i == 1, Keyframe: id == 0xa3, Discardable: id == 0xa3, Data: [][]byte{sample}})
		if id == 0xa1 {
			block = ebmlElement(0xa0, block)
		}
		blocks = append(blocks, block...)
	}
	return mkvTestFile(config, 0, ebmlElement(mkvCluster, ebmlCRC(ebmlUint(0xe7, 100), blocks)), nil, true, false)
}

func TestMatroskaUnlacedBlockStorage(t *testing.T) {
	want, err := Extract(t.Context(), bytes.NewReader(blockStorageFixture(t, []int{0, 0, 0})), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer want.memory.release()
	wantModel, err := want.PlotMetadata()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		sizes []int
		limit int64
	}{{[]int{3 << 20, 3 << 20, 3 << 20}, 6 << 20}, {[]int{2 << 20, 1 << 20, 4 << 20}, 8 << 20}} {
		budget, _ := NewMemoryBudget(tc.limit)
		got, err := Extract(t.Context(), bytes.NewReader(blockStorageFixture(t, tc.sizes)), Options{Budget: budget})
		if err != nil {
			t.Fatalf("sizes=%v: %v", tc.sizes, err)
		}
		model, err := got.PlotMetadata()
		if err != nil || !reflect.DeepEqual(got.Frames, want.Frames) || !reflect.DeepEqual(got.Payloads, want.Payloads) || !reflect.DeepEqual(got.SceneStarts, want.SceneStarts) || !reflect.DeepEqual(model, wantModel) {
			t.Fatalf("block reuse changed timing, visibility or metadata: error=%v", err)
		}
		got.memory.release()
		if used, peak := budget.Usage(); used != 0 || peak > tc.limit {
			t.Fatalf("storage cleanup: used=%d peak=%d", used, peak)
		}
	}
}

func TestMatroskaLargeBlockErrors(t *testing.T) {
	config, sample := largePictureSample(t, 512<<10)
	for _, tc := range []struct {
		name string
		tail []byte
		want error
	}{
		{"unescaped-body", []byte{0, 0, 2, 0x55}, ErrInvalidBitstream},
		{"terminal-zero", []byte{0}, ErrInvalidBitstream},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broken := bytes.Clone(sample)
			pos := 0
			for pos+4+int(binary.BigEndian.Uint32(broken[pos:pos+4])) < len(broken) {
				pos += 4 + int(binary.BigEndian.Uint32(broken[pos:pos+4]))
			}
			length := binary.BigEndian.Uint32(broken[pos : pos+4])
			broken = append(broken, tc.tail...)
			binary.BigEndian.PutUint32(broken[pos:pos+4], length+uint32(len(tc.tail)))
			block := mkvTestBlock(t, 0xa3, ebml.Block{TrackNumber: 1, Keyframe: true, Data: [][]byte{broken}})
			data := mkvTestFile(config, 0, ebmlElement(mkvCluster, ebmlCRC(ebmlUint(0xe7, 0), block)), nil, false, false)
			budget, _ := NewMemoryBudget(4 << 20)
			got, err := Extract(t.Context(), bytes.NewReader(data), Options{Budget: budget})
			if got != nil || !errors.Is(err, tc.want) {
				t.Fatalf("result=%v error=%v", got, err)
			}
			if used, _ := budget.Usage(); used != 0 {
				t.Fatalf("error retained %d bytes", used)
			}
		})
	}
}

func BenchmarkMatroskaBlockStorage(b *testing.B) {
	data := blockStorageFixture(b, []int{512 << 10, 512 << 10, 512 << 10})
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		got, err := Extract(b.Context(), bytes.NewReader(data), Options{})
		if err != nil {
			b.Fatal(err)
		}
		got.memory.release()
	}
}
