package extract

import (
	"bytes"
	"slices"
	"testing"
)

func TestSignedPOCWrapAndLeadingPictures(t *testing.T) {
	sets, metadata, _ := headerFixtureParts(t)
	for _, tc := range []struct {
		name  string
		kinds []byte
		pocs  []uint64
		want  []int64
	}{
		{"wrap", []byte{19, 1, 1, 1}, []uint64{0, 127, 250, 2}, []int64{0, 127, 250, 258}},
		{"RADL", []byte{21, 7, 1}, []uint64{0, 255, 1}, []int64{-1, 0, 1}},
		{"RASL", []byte{21, 9, 1}, []uint64{0, 255, 1}, []int64{0, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := append(slices.Clone(sets), metadata...)
			for i, poc := range tc.pocs {
				data = append(data, authoredReferencePicture(poc, tc.kinds[i], 2, nil, nil)...)
			}
			e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
			if err != nil {
				t.Fatal(err)
			}
			var got []int64
			for _, f := range e.Frames {
				got = append(got, f.POC)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("POCs%v want%v", got, tc.want)
			}
		})
	}
}
func TestDependentAndIndependentSlicesShareOnePicture(t *testing.T) {
	sets, metadata, _ := headerFixtureParts(t)
	for _, dependent := range []bool{false, true} {
		data := append(slices.Clone(sets), metadata...)
		data = append(data, authoredReferencePicture(0, 19, 2, nil, nil)...)
		bits := "00" + ueBits(5)
		if dependent {
			bits += "11"
		} else {
			bits += "01" + ueBits(2) + "1" + ueBits(0)
		}
		bits += "1"
		data = append(data, authoredNAL(19, append(packedHeader(bits), 0xff))...)
		e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
		if err != nil {
			t.Fatalf("dependent%v: %v", dependent, err)
		}
		if len(e.Frames) != 1 || e.Frames[0].AUOrdinal != 0 {
			t.Fatal("slice became a frame")
		}
	}
}
func TestNonOutputUpdatesAndPriorOutputSuppression(t *testing.T) {
	sets, metadata, _ := headerFixtureParts(t)
	data := append(slices.Clone(sets), metadata...)
	data = append(data, authoredReferencePicture(0, 19, 2, nil, nil)...)
	data = append(data, authoredSEI(t, "profile-b")...)
	bits := "1" + ueBits(5) + ueBits(2) + "0" + "00000010" + "0" + ueBits(0) + ueBits(0) + ueBits(0) + "1"
	data = append(data, authoredNAL(1, append(packedHeader(bits), 0xff))...)
	data = append(data, authoredReferencePicture(1, 1, 2, nil, nil)...)
	e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Frames) != 2 || e.Frames[1].AUOrdinal != 2 || e.Payloads[e.Frames[1].PayloadIndex].profile() != "B" {
		t.Fatalf("hidden update %+v", e)
	}
	data = append(data, authoredSEI(t, "profile-na")...)
	bits = "11" + ueBits(5) + ueBits(2) + "1" + ueBits(0) + "1"
	data = append(data, authoredNAL(19, append(packedHeader(bits), 0xff))...)
	e, err = Extract(t.Context(), bytes.NewReader(data), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Frames) != 1 || e.Frames[0].AUOrdinal != 3 || e.Payloads[e.Frames[0].PayloadIndex].profile() != "N/A" {
		t.Fatalf("prior output suppression %+v", e)
	}
}
