package extract

import (
	"bytes"
	"fmt"
	"math/bits"
	"reflect"
	"slices"
	"testing"
)

// Each picture contains two independent slices. The mixed case adds a
// dependent segment to each slice. Nonzero spatial segmentation makes losing
// an independent slice observable without inspecting entropy-coded pixels.
func TestMultislicePlaylistPrefixes(t *testing.T) {
	for _, name := range []string{"first", "last", "tail", "repeated", "seamless", "lookahead", "dependent-segments"} {
		t.Run(name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.vui = "0000000001000" + ueBits(1) + ueBits(0) + ueBits(0) + ueBits(15) + ueBits(15)
			addresses := []uint64{0, 1}
			if name == "dependent-segments" {
				s.width = 512
				addresses = []uint64{0, 1, 4, 5}
			}
			picture := func(poc, ref uint64, kind byte, inter bool) []byte {
				var data []byte
				for i, address := range addresses {
					h := "1"
					if i > 0 {
						h = "0"
					}
					if kind == 19 {
						h += "0"
					}
					h += ueBits(5)
					dependent := name == "dependent-segments" && i%2 == 1
					if i > 0 {
						if dependent {
							h += "1"
						} else {
							h += "0"
						}
						h += fmt.Sprintf("%0*b", bits.Len64(s.width/64-1), address)
					}
					if !dependent {
						slice := uint64(2)
						if inter {
							slice = 1
						}
						h += ueBits(slice) + "1"
						if kind != 19 {
							h += fmt.Sprintf("%08b", poc) + "0" + ueBits(1) + ueBits(0) + ueBits(poc-ref-1)
							if inter {
								h += "1" + "0" + ueBits(0) // Used reference, default list, merge count.
							} else {
								h += "0"
							}
						}
						h += seBits(0)
					}
					data = append(data, authoredNAL(kind, append(packedHeader(h+"1"), 0xff, 0xff))...)
				}
				return data
			}
			raw := parameterSource(t, s, conformancePPS(0, 0, 0, false))
			raw = raw[:bytes.Index(raw, []byte{0, 0, 1, 38, 1})]
			var markers []Marker
			for i, poc := range []uint64{0, 1, 3} {
				kind, ref := byte(1), uint64(0)
				if i == 0 {
					kind = 19
				} else {
					profile := "profile-b"
					if i == 2 {
						profile, ref = "profile-na", 1
					}
					raw = append(raw, authoredSEI(t, profile)...)
				}
				markers = append(markers, Marker{ESOffset: uint64(len(raw) + 3), Kind: PESStart, PTS: Timestamp{Value: int64(i * 3000), Timescale: 90000, Valid: true}})
				raw = append(raw, picture(poc, ref, kind, false)...)
			}
			direct, err := Extract(t.Context(), bytes.NewReader(raw), Options{})
			if err != nil {
				t.Fatalf("lawful complete source rejected: %v", err)
			}
			defer direct.memory.release()
			var pocs []int64
			for _, frame := range direct.Frames {
				pocs = append(pocs, frame.POC)
			}
			if !slices.Equal(pocs, []int64{0, 1, 3}) || direct.Profile != "N/A" {
				t.Fatalf("incorrect complete source: %+v", direct)
			}
			collect := func(key StreamKey, data []byte, timing []Marker) *Clip {
				stream, err := NewStream(StreamOptions{Key: key, RetainBoundarySyntax: true})
				if err != nil {
					t.Fatal(err)
				}
				if err = stream.Push(t.Context(), Chunk{Data: data, Markers: timing}); err != nil {
					t.Fatal(err)
				}
				clip, err := stream.Finish(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(clip.memory.release)
				return clip
			}
			key := StreamKey{ClipID: "multislice"}
			clips := map[StreamKey]*Clip{key: collect(key, raw, markers)}
			p := Playlist{Occurrences: []Occurrence{{Key: key, In: 0, Out: 1500, ClockOffset: Timestamp{Timescale: 1, Valid: true}, Connection: ConnectionReset}}}
			wantPayloads, wantAUs := []uint64{0}, []uint64{0}
			wantProfile, wantScenes := "A", []uint64{0}
			if name == "last" {
				p.Occurrences[0].In, p.Occurrences[0].Out = 1500, 3000
				wantPayloads[0], wantAUs[0], wantProfile = 1, 1, "B"
			}
			if name == "tail" {
				p.Occurrences[0].In, p.Occurrences[0].Out = 3000, 4500
				wantPayloads[0], wantAUs[0], wantProfile = 2, 2, "N/A"
			}
			if name == "repeated" {
				second := p.Occurrences[0]
				second.PlaylistStart = 1500
				p.Occurrences = append(p.Occurrences, second)
				wantPayloads, wantAUs = []uint64{0, 0}, []uint64{0, 0}
			}
			if name == "seamless" || name == "lookahead" {
				destination := StreamKey{ClipID: "destination"}
				poc, inter := uint64(1), false
				wantPayloads = []uint64{0, 0}
				if name == "lookahead" {
					poc, inter = 2, true
					wantPayloads[1], wantProfile, wantScenes = 1, "N/A", []uint64{0, 1}
				}
				clips[destination] = collect(destination, picture(poc, poc-1, 1, inter), []Marker{{ESOffset: 3, Kind: PESStart, PTS: Timestamp{Timescale: 90000, Valid: true}}})
				p.Occurrences = append(p.Occurrences, Occurrence{Key: destination, Out: 1500, PlaylistStart: 1500, ClockOffset: Timestamp{Timescale: 1, Valid: true}, Connection: ConnectionSeamless})
				wantAUs = []uint64{0, 0}
			}
			out, err := Assemble(t.Context(), p, clips, AssemblyOptions{})
			if err != nil {
				t.Fatalf("lawful multislice playlist rejected: %v", err)
			}
			defer out.memory.release()
			if len(out.Frames) != len(wantPayloads) || out.Profile != wantProfile || !slices.Equal(out.SceneStarts, wantScenes) {
				t.Fatalf("incorrect complete timeline: %+v", out)
			}
			for i, frame := range out.Frames {
				want := direct.Payloads[direct.Frames[wantPayloads[i]].PayloadIndex]
				if frame.AUOrdinal != wantAUs[i] || frame.OccurrenceOrdinal != uint64(i) || frame.Stream != p.Occurrences[i].Key || compareTime(frame.PTS, Timestamp{Value: int64(i * 1500), Timescale: 45000, Valid: true}) != 0 || !reflect.DeepEqual(out.Payloads[frame.PayloadIndex], want) {
					t.Fatalf("incorrect selected frame %d: %+v", i, frame)
				}
			}
			if _, err := out.PlotMetadata(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
