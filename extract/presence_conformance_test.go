package extract

import (
	"errors"
	"slices"
	"testing"
)

func TestTrimmedMetadataPresence(t *testing.T) {
	sets, metadata, picture := headerFixtureParts(t)
	for _, tc := range []struct {
		name                  string
		before, after, hidden bool
		in, out               int64
		want                  error
		wantAU                uint64
	}{
		{name: "all-absent", out: 1500, want: ErrNoMetadata},
		{name: "update-in-discarded-suffix", after: true, out: 1500, want: ErrIncomplete},
		{name: "hidden-update-in-discarded-suffix", after: true, hidden: true, out: 1500, want: ErrIncomplete},
		{name: "metadata-before-trim", before: true, in: 1500, out: 3000, wantAU: 1},
		{name: "unknown-before-trim", after: true, in: 1500, out: 3000, wantAU: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := slices.Clone(sets)
			if tc.before {
				data = append(data, metadata...)
			}
			data = append(data, picture...)
			if tc.after {
				data = append(data, metadata...)
			}
			next := authoredReferencePicture(1, 1, 2, nil, nil)
			if tc.hidden {
				header := "1" + ueBits(5) + ueBits(2) + "0" + "00000001" + "0" + ueBits(0) + ueBits(0) + seBits(0) + "1"
				next = authoredNAL(1, append(packedHeader(header), 0xff, 0xff))
			}
			data = append(data, next...)
			key := StreamKey{ClipID: tc.name}
			clip := sourceClip(t, key, data, []int64{0, 3000})
			defer clip.memory.release()
			playlist := Playlist{Occurrences: []Occurrence{{Key: key, In: tc.in, Out: tc.out, ClockOffset: Timestamp{Timescale: 1, Valid: true}, Connection: ConnectionReset}}}
			result, err := Assemble(t.Context(), playlist, map[StreamKey]*Clip{key: clip}, AssemblyOptions{})
			if tc.want != nil {
				if result != nil || !errors.Is(err, tc.want) || tc.want == ErrIncomplete && errors.Is(err, ErrNoMetadata) {
					t.Fatalf("want %v: %v %v", tc.want, result, err)
				}
				return
			}
			if err != nil || len(result.Frames) != 1 || result.Frames[0].AUOrdinal != tc.wantAU || result.Payloads[result.Frames[0].PayloadIndex].profile() != "A" {
				t.Fatalf("trimmed inheritance: %v %v", result, err)
			}
		})
	}
}
