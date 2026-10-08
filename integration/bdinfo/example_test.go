// SPDX-License-Identifier: GPL-2.0-or-later
package bdinfo_test

import (
	"context"
	"fmt"
	bridge "github.com/Audionut/go-hdr10-plus/integration/bdinfo"
	scanner "github.com/autobrr/go-bdinfo/pkg/bdinfo"
)

func ExampleScan() {
	out, err := bridge.Scan(context.Background(), bridge.Options{BDInfo: scanner.Options{Path: `E:\Movies\Disc`, Settings: scanner.DefaultSettings(".")}})
	if err != nil {
		return
	}
	fmt.Println(out.Report.Report)
	for _, playlist := range out.Playlists {
		if playlist.Err != nil {
			fmt.Println(playlist.Name, playlist.Err)
			continue
		}
		metadata, err := playlist.Extraction.PlotMetadata()
		if err != nil {
			return
		}
		fmt.Println(playlist.Name, len(metadata.Frames))
	}
}
