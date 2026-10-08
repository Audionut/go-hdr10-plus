package extract_test

import (
	"context"
	"image/png"
	"os"

	hdr10plus "github.com/Audionut/go-hdr10-plus"
	"github.com/Audionut/go-hdr10-plus/extract"
)

func ExampleExtract() {
	file, err := os.Open("movie.mkv")
	if err != nil {
		return
	}
	defer file.Close()
	result, err := extract.Extract(context.Background(), file, extract.Options{})
	if err != nil {
		return
	}
	metadata, err := result.PlotMetadata()
	if err != nil {
		return
	}
	image, err := hdr10plus.Render(metadata, hdr10plus.Options{Title: "Movie"})
	if err != nil {
		return
	}
	output, err := os.Create("brightness.png")
	if err != nil {
		return
	}
	defer output.Close()
	if err := png.Encode(output, image); err != nil {
		return
	}
}
