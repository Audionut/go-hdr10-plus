package hdr10plus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Upstream input assets stay outside the repository. Set this directory to the
// pinned checkout's assets/hevc_tests to reproduce the differential check.
func TestUpstreamDifferential(t *testing.T) {
	directory := os.Getenv("HDR10PLUS_REFERENCE_DIR")
	if directory == "" {
		t.Skip("set HDR10PLUS_REFERENCE_DIR for pinned upstream input assets")
	}
	input, err := os.ReadFile("testdata/numeric-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference struct {
		Files []struct {
			Name    string
			Profile string
			Frames  int
			Scenes  int
			Checks  []struct {
				Start, End int
				Sources    []referenceStats
			}
		}
	}
	if err := json.Unmarshal(input, &reference); err != nil {
		t.Fatal(err)
	}
	for _, file := range reference.Files {
		t.Run(file.Name, func(t *testing.T) {
			input, err := os.Open(filepath.Join(directory, file.Name))
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := DecodeJSON(input)
			input.Close()
			if err != nil {
				t.Fatal(err)
			}
			if len(metadata.Frames) != file.Frames || metadata.Profile != file.Profile || metadata.SceneCount != file.Scenes {
				t.Fatalf("root projection: %d %s %d", len(metadata.Frames), metadata.Profile, metadata.SceneCount)
			}
			for _, check := range file.Checks {
				for source, want := range check.Sources {
					data, err := calculate(metadata.Frames[check.Start:check.End+1], check.Start, PeakSource(source))
					if err != nil {
						t.Fatal(err)
					}
					assertNits(t, data.average.maximum, want.AverageMax)
					assertNits(t, data.average.mean, want.AverageMean)
					assertNits(t, data.peak.maximum, want.PeakMax)
					assertNits(t, data.peak.mean, want.PeakMean)
				}
			}
		})
	}
}
