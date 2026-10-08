package extract

import (
	"errors"
	"testing"
)

func TestObservedSourcePacketExtent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		start, end uint64
		observed   bool
		valid      bool
	}{
		{"whole-source", 0, 10, true, true},
		{"observed-PES-start", 2, 10, true, true},
		{"unproven-start", 1, 10, true, false},
		{"unproven-end", 0, 9, true, false},
		{"beyond-EOF", 0, 11, true, false},
		{"unknown-final-extent", 0, 10, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := rawFixture(t, "single")
			header := tsPES(raw, 0, 0, true)[:19]
			key := StreamKey{ClipID: "source", TrackID: 4113}
			s, err := NewStream(StreamOptions{Key: key, RetainBoundarySyntax: true})
			if err != nil {
				t.Fatal(err)
			}
			defer s.memory.release()
			marker := Marker{Kind: PESStart, HasSourcePosition: true, PacketIndex: 2, Modulo33: true, PTS: Timestamp{Timescale: 90000, Valid: true}, DTS: Timestamp{Timescale: 90000, Valid: true}, PESHeader: header}
			if err := s.Push(t.Context(), Chunk{Data: raw, Markers: []Marker{marker}}); err != nil {
				t.Fatal(err)
			}
			// Header storage belongs to the producer after Push; optional fields
			// must already have been validated and removed from retained markers.
			header[6] = 0
			if len(s.markers[0].PESHeader) != 0 {
				t.Fatal("retained borrowed PES header")
			}
			if tc.observed {
				end := Marker{Kind: SourceEnd, ESOffset: uint64(len(raw)), HasSourcePosition: true, PacketIndex: 10}
				if err := s.Push(t.Context(), Chunk{ESOffset: uint64(len(raw)), Markers: []Marker{end}}); err != nil {
					t.Fatal(err)
				}
			}
			clip, err := s.Finish(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			p := Playlist{Occurrences: []Occurrence{{Key: key, Out: 1500, Connection: ConnectionReset, ClockOffset: Timestamp{Timescale: 1, Valid: true}, Packets: &PacketRange{Start: tc.start, End: tc.end, ClockAnchor: Timestamp{Timescale: 90000, Valid: true}}}}}
			out, err := Assemble(t.Context(), p, map[StreamKey]*Clip{key: clip}, AssemblyOptions{})
			if !tc.valid {
				if out != nil || !errors.Is(err, ErrUnsupportedInput) {
					t.Fatalf("uncertified extent completed: %v %v", out != nil, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer out.memory.release()
			if len(out.Frames) != 1 || out.Frames[0].AUOrdinal != 0 || out.Profile != "A" {
				t.Fatalf("changed valid source projection: %+v", out)
			}
		})
	}
}

func TestSourceEndIsTerminal(t *testing.T) {
	raw := rawFixture(t, "single")
	s, err := NewStream(StreamOptions{RetainBoundarySyntax: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Push(t.Context(), Chunk{Data: raw}); err != nil {
		t.Fatal(err)
	}
	marker := Marker{Kind: SourceEnd, ESOffset: uint64(len(raw)), HasSourcePosition: true, PacketIndex: 1}
	if err := s.Push(t.Context(), Chunk{ESOffset: uint64(len(raw)), Markers: []Marker{marker}}); err != nil {
		t.Fatal(err)
	}
	err = s.Push(t.Context(), Chunk{ESOffset: uint64(len(raw)), Data: []byte{0xff}})
	if !errors.Is(err, ErrIncomplete) {
		t.Fatal(err)
	}
	if clip, end := s.Finish(t.Context()); clip != nil || end != err {
		t.Fatalf("terminal source failure lost: %v %v", clip, end)
	}
}
