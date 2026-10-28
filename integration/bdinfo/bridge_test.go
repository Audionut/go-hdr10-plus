// SPDX-License-Identifier: GPL-2.0-or-later
package bdinfo

import (
	"errors"
	"testing"

	"github.com/Audionut/go-hdr10-plus/extract"
	scanner "github.com/autobrr/go-bdinfo/pkg/bdinfo"
)

func TestScanFolderReportAndMetadata(t *testing.T) {
	root := discFixture(t)
	settings := scanner.DefaultSettings(".")
	settings.FilterLoopingPlaylists = false
	settings.FilterShortPlaylists = false
	settings.BigPlaylistOnly = false
	opts := Options{BDInfo: scanner.Options{Path: root, Settings: settings}}
	base, err := scanner.Run(t.Context(), opts.BDInfo)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Scan(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if out.Report.Report != base.Report || out.Report.QuickSummary != base.QuickSummary || out.Report.ForumsBlock != base.ForumsBlock {
		t.Fatal("report changed")
	}
	if len(out.SourceErrors) != 0 || len(out.Playlists) != 2 || len(out.Report.Collection) != 1 || !out.Report.Collection[0].CleanEOF {
		t.Fatalf("outcome %#v sources %#v collection %#v", out, out.SourceErrors, out.Report.Collection)
	}
	for _, p := range out.Playlists {
		if p.Err != nil {
			t.Fatal(p.Err)
		}
		if len(p.Extraction.Frames) != 6 {
			t.Fatalf("frames %d", len(p.Extraction.Frames))
		}
		want := []uint64{0, 2, 1, 0, 2, 1}
		for i, f := range p.Extraction.Frames {
			if f.AUOrdinal != want[i] || f.OccurrenceOrdinal != uint64(i/3) {
				t.Fatalf("frame %d: %#v", i, f)
			}
		}
		m, err := p.Extraction.PlotMetadata()
		if err != nil {
			t.Fatal(err)
		}
		if m.Profile != "N/A" || m.SceneCount != 5 {
			t.Fatalf("metadata %#v", m)
		}
	}
	// Exhaustion is optional-extraction failure; it does not rerun BDInfo or
	// prevent its ordinary report from completing.
	opts.MaxScanRetainedBytes = 16384
	limited, err := Scan(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if limited.Report.Report != base.Report || limited.DeclinedAfterExhaustion != 1 {
		t.Fatalf("limited %#v", limited)
	}
	for _, p := range limited.Playlists {
		if p.Extraction != nil || !errors.Is(p.Err, extract.ErrResourceLimit) {
			t.Fatalf("partial playlist %#v", p)
		}
	}
}

func TestTimelineDefaultAngle(t *testing.T) {
	settings := scanner.DefaultSettings(".")
	settings.FilterLoopingPlaylists = false
	settings.FilterShortPlaylists = false
	settings.BigPlaylistOnly = false
	report, err := scanner.Run(t.Context(), scanner.Options{Path: discFixture(t), Settings: settings, IncludeTimeline: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Timelines) == 0 || len(report.Timelines[0].Items) == 0 || len(report.Timelines[0].Items[0].Angles) == 0 {
		t.Fatal("fixture has no default angle")
	}
	timeline := report.Timelines[0]
	original := timeline.Items[0].Angles[0]
	alternative := original
	alternative.Index = 1
	alternative.Source.Path = "alternate.m2ts"
	timeline.Items[0].Angles = append(timeline.Items[0].Angles, alternative)
	playlist, err := Timeline("source", timeline)
	if err != nil {
		t.Fatal(err)
	}
	if len(playlist.Occurrences) != len(timeline.Items) || playlist.Occurrences[0].Key.ClipID != original.Source.Path || playlist.Occurrences[0].Angle != uint32(original.Index) {
		t.Fatalf("default angle changed: %#v", playlist)
	}
	timeline.Items[0].Angles = nil
	if _, err := Timeline("source", timeline); !errors.Is(err, extract.ErrUnsupportedInput) {
		t.Fatalf("missing angle: got %v, want unsupported input", err)
	}
}
