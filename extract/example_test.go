package extract_test

import (
	"fmt"
	"os"

	"github.com/Audionut/go-hdr10-plus/extract"
)

func ExampleDecodeT35() {
	data, err := os.ReadFile("testdata/profile-a.t35")
	if err != nil {
		panic(err)
	}
	payload, err := extract.DecodeT35(data)
	if err != nil {
		panic(err)
	}
	fmt.Println(payload.AverageRGB, payload.Distributions[8].Index)
	// Output: 12345 99
}

func ExampleExtraction_PlotMetadata() {
	data, err := os.ReadFile("testdata/profile-a.t35")
	if err != nil {
		panic(err)
	}
	payload, err := extract.DecodeT35(data)
	if err != nil {
		panic(err)
	}
	result := &extract.Extraction{
		Payloads: []extract.Payload{*payload},
		Frames:   []extract.Picture{{PayloadIndex: 0}},
		Profile:  "A", SceneStarts: []uint64{0},
	}
	metadata, err := result.PlotMetadata()
	if err != nil {
		panic(err)
	}
	fmt.Println(metadata.Profile, metadata.SceneCount, metadata.Frames[0].AverageRGB)
	// Output: A 1 12345
}
