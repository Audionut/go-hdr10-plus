package extract

import (
	"bytes"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func ueBits(v uint64) string {
	bits := strconv.FormatUint(v+1, 2)
	return strings.Repeat("0", len(bits)-1) + bits
}

type shortReference struct {
	delta int16
	used  bool
}

func authoredReferencePicture(poc uint64, kind byte, slice uint64, negative, positive []shortReference) []byte {
	bits := "1"
	if kind >= 16 {
		bits += "0"
	}
	bits += ueBits(5) + ueBits(slice) + "1"
	if kind != 19 && kind != 20 {
		bits += strconv.FormatUint(poc|256, 2)[1:] + "0" + ueBits(uint64(len(negative))) + ueBits(uint64(len(positive)))
		previous := int16(0)
		for _, ref := range negative {
			bits += ueBits(uint64(previous - ref.delta - 1))
			if ref.used {
				bits += "1"
			} else {
				bits += "0"
			}
			previous = ref.delta
		}
		previous = 0
		for _, ref := range positive {
			bits += ueBits(uint64(ref.delta - previous - 1))
			if ref.used {
				bits += "1"
			} else {
				bits += "0"
			}
			previous = ref.delta
		}
		if slice != 2 {
			bits += "0"
			if slice == 0 {
				bits += "0"
			}
			bits += ueBits(0)
		}
	}
	bits += ueBits(0) + "1" // QP0 and byte_alignment
	return authoredNAL(kind, append(packedHeader(bits), 0xff, 0xff))
}
func authoredSEI(t testing.TB, name string) []byte {
	t.Helper()
	p := fixture(t, name)
	rbsp := []byte{4, byte(len(p))}
	rbsp = append(rbsp, p...)
	return authoredNAL(39, append(rbsp, 0x80))
}
func sourceClip(t *testing.T, key StreamKey, data []byte, pts []int64) *Clip {
	t.Helper()
	var markers []Marker
	for pos := 0; pos < len(data); {
		end := len(data)
		next := bytes.Index(data[pos+3:], []byte{0, 0, 1})
		if next >= 0 {
			end = pos + 3 + next
		}
		if data[pos+3]>>1&63 <= 31 {
			markers = append(markers, Marker{ESOffset: uint64(pos), Kind: PESStart, PTS: Timestamp{Value: pts[len(markers)], Timescale: 90000, Valid: true}})
		}
		pos = end
	}
	s, err := NewStream(StreamOptions{Key: key, RetainBoundarySyntax: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Push(t.Context(), Chunk{Data: data, Markers: markers}); err != nil {
		t.Fatal(err)
	}
	c, err := s.Finish(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.memory.release)
	return c
}
func TestSeamlessReferenceLookaheadAndRepeatedPredecessors(t *testing.T) {
	sets, _, _ := headerFixtureParts(t)
	makePredecessor := func(name string) []byte {
		data := slices.Clone(sets)
		data = append(data, authoredSEI(t, "profile-a")...)
		data = append(data, authoredReferencePicture(0, 19, 2, nil, nil)...)
		data = append(data, authoredSEI(t, name)...)
		data = append(data, authoredReferencePicture(2, 1, 2, []shortReference{{delta: -2}}, nil)...)
		// This later, unneeded update must never seed the destination.
		data = append(data, authoredSEI(t, "profile-a")...)
		data = append(data, authoredReferencePicture(4, 1, 2, []shortReference{{delta: -2}, {delta: -4}}, nil)...)
		return data
	}
	keys := []StreamKey{{ClipID: "predecessor-B"}, {ClipID: "predecessor-NA"}, {ClipID: "destination"}}
	clips := map[StreamKey]*Clip{
		keys[0]: sourceClip(t, keys[0], makePredecessor("profile-b"), []int64{0, 6000, 12000}),
		keys[1]: sourceClip(t, keys[1], makePredecessor("profile-na"), []int64{0, 6000, 12000}),
		keys[2]: sourceClip(t, keys[2], authoredReferencePicture(1, 1, 0, []shortReference{{delta: -1, used: true}}, []shortReference{{delta: 1, used: true}}), []int64{0}),
	}
	p := Playlist{Occurrences: []Occurrence{
		{Key: keys[0], In: 0, Out: 1500, ClockOffset: Timestamp{Timescale: 1, Valid: true}, Connection: ConnectionReset},
		{Key: keys[2], In: 0, Out: 1500, PlaylistStart: 1500, ClockOffset: Timestamp{Timescale: 1, Valid: true}, Connection: ConnectionSeamless},
		{Key: keys[1], In: 0, Out: 1500, PlaylistStart: 3000, ClockOffset: Timestamp{Timescale: 1, Valid: true}, Connection: ConnectionReset},
		{Key: keys[2], In: 0, Out: 1500, PlaylistStart: 4500, ClockOffset: Timestamp{Timescale: 1, Valid: true}, Connection: ConnectionSeamless},
	}}
	e, err := Assemble(t.Context(), p, clips, AssemblyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Frames) != 4 {
		t.Fatalf("frames%+v", e.Frames)
	}
	for i, profile := range []string{"A", "B", "A", "N/A"} {
		f := e.Frames[i]
		if e.Payloads[f.PayloadIndex].profile() != profile || f.OccurrenceOrdinal != uint64(i) || f.AUOrdinal != 0 || compareTime(f.PTS, Timestamp{Value: int64(i * 3000), Timescale: 90000, Valid: true}) != 0 {
			t.Fatalf("frame%d %+v profile%s", i, f, e.Payloads[f.PayloadIndex].profile())
		}
	}
	p.Occurrences[1].Connection = ConnectionReset
	if _, err := Assemble(t.Context(), p, clips, AssemblyOptions{}); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("reset borrowed state: %v", err)
	}
}
