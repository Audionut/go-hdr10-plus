// SPDX-License-Identifier: GPL-2.0-or-later
package bdinfo

import (
	scanner "github.com/autobrr/go-bdinfo/pkg/bdinfo"
	"os"
	"testing"
)

func BenchmarkScan(b *testing.B) {
	path := os.Getenv("HDR10PLUS_BDINFO_BENCHMARK_PATH")
	if path == "" {
		path = discFixture(b)
	}
	settings := scanner.DefaultSettings(".")
	settings.FilterLoopingPlaylists = false
	settings.FilterShortPlaylists = false
	settings.BigPlaylistOnly = false
	options := Options{BDInfo: scanner.Options{Path: path, Settings: settings}}
	b.Run("report", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := scanner.Run(b.Context(), options.BDInfo); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("report-plus-extraction", func(b *testing.B) {
		b.ReportAllocs()
		var peak int64
		var bytes, streams uint64
		for b.Loop() {
			out, err := Scan(b.Context(), options)
			if err != nil {
				b.Fatal(err)
			}
			for _, p := range out.Playlists {
				if p.Err != nil {
					b.Fatal(p.Err)
				}
			}
			peak = out.PeakRetainedBytes
			streams = uint64(len(out.Report.Collection))
			bytes = 0
			for _, s := range out.Report.Collection {
				bytes += s.DeliveredBytes
			}
		}
		b.ReportMetric(float64(peak), "accounted-peak-B")
		b.ReportMetric(float64(streams), "physical-collectors/op")
		b.ReportMetric(float64(bytes), "delivered-ES-B/op")
	})
}
