package hdr10plus

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

const minimalJSON = `{"JSONInfo":{"HDR10plusProfile":""},"SceneInfo":[{"LuminanceParameters":{"AverageRGB":0}}],"SceneInfoSummary":{"SceneFrameNumbers":[]}}`

func TestDecodeJSONProjection(t *testing.T) {
	metadata, err := DecodeJSON(strings.NewReader(minimalJSON + " \n\t"))
	if err != nil {
		t.Fatal(err)
	}
	want := &Metadata{Frames: []Frame{{}}}
	if !reflect.DeepEqual(metadata, want) {
		t.Fatalf("got %#v, want %#v", metadata, want)
	}
	input, err := os.ReadFile("testdata/minimal.json")
	if err != nil {
		t.Fatal(err)
	}
	metadata, err = DecodeJSON(strings.NewReader(string(input)))
	if err != nil {
		t.Fatal(err)
	}
	want = &Metadata{Profile: "A", SceneCount: 2, Frames: []Frame{
		{100, []uint32{9000, 100, 5000}, []uint32{1000, 2000, 3000}},
		{5000, []uint32{100, 10000, 8000}, []uint32{7000, 5000, 9000}},
		{2000, []uint32{100, 7000, 6000}, []uint32{5000, 4000, 6000}},
	}}
	if !reflect.DeepEqual(metadata, want) {
		t.Fatalf("projection mismatch: %#v", metadata)
	}
	// Unknown extensions are ignored; matching and duplicate processing follow
	// encoding/json, including case-insensitive field names and last scalars.
	input = []byte(strings.ReplaceAll(minimalJSON, `"AverageRGB":0`, `"averagergb":12,"AverageRGB":34,"Unknown":[true,null]`))
	metadata, err = DecodeJSON(strings.NewReader(string(input)))
	if err != nil || metadata.Frames[0].AverageRGB != 34 {
		t.Fatalf("duplicate/case behavior: %v, %v", metadata, err)
	}
	for _, optional := range []string{`"MaxScl":null`, `"MaxScl":[]`, `"LuminanceDistributions":null`, `"LuminanceDistributions":{}`, `"LuminanceDistributions":{"DistributionValues":null}`} {
		input := strings.Replace(minimalJSON, `"AverageRGB":0`, `"AverageRGB":0,`+optional, 1)
		if _, err := DecodeJSON(strings.NewReader(input)); err != nil {
			t.Fatalf("optional %s: %v", optional, err)
		}
	}
}

func TestDecodeJSONRejectsInvalidFields(t *testing.T) {
	cases := []struct{ name, input, path string }{
		{"empty", "", "root"},
		{"null root", "null", "root"},
		{"array root", "[]", "root"},
		{"missing info", strings.Replace(minimalJSON, `"JSONInfo":{"HDR10plusProfile":""},`, "", 1), "JSONInfo"},
		{"null info", strings.Replace(minimalJSON, `{"HDR10plusProfile":""}`, "null", 1), "JSONInfo"},
		{"missing profile", strings.Replace(minimalJSON, `"HDR10plusProfile":""`, "", 1), "HDR10plusProfile"},
		{"null profile", strings.Replace(minimalJSON, `"HDR10plusProfile":""`, `"HDR10plusProfile":null`, 1), "HDR10plusProfile"},
		{"wrong profile", strings.Replace(minimalJSON, `"HDR10plusProfile":""`, `"HDR10plusProfile":1`, 1), "JSONInfo"},
		{"empty frames", strings.Replace(minimalJSON, `[{"LuminanceParameters":{"AverageRGB":0}}]`, "[]", 1), "SceneInfo"},
		{"null frame", strings.Replace(minimalJSON, `{"LuminanceParameters":{"AverageRGB":0}}`, "null", 1), "SceneInfo[0]"},
		{"null luminance", strings.Replace(minimalJSON, `{"AverageRGB":0}`, "null", 1), "SceneInfo[0].LuminanceParameters"},
		{"missing average", strings.Replace(minimalJSON, `"AverageRGB":0`, "", 1), "AverageRGB"},
		{"null average", strings.Replace(minimalJSON, `"AverageRGB":0`, `"AverageRGB":null`, 1), "AverageRGB"},
		{"summary null", strings.Replace(minimalJSON, `{"SceneFrameNumbers":[]}`, "null", 1), "SceneInfoSummary"},
		{"summary missing array", strings.Replace(minimalJSON, `"SceneFrameNumbers":[]`, "", 1), "SceneFrameNumbers"},
		{"summary null array", strings.Replace(minimalJSON, `"SceneFrameNumbers":[]`, `"SceneFrameNumbers":null`, 1), "SceneFrameNumbers"},
		{"summary null number", strings.Replace(minimalJSON, `"SceneFrameNumbers":[]`, `"SceneFrameNumbers":[null]`, 1), "SceneFrameNumbers[0]"},
		{"second value", minimalJSON + " {}", "one JSON"},
		{"garbage", minimalJSON + " x", "after root"},
	}
	for _, value := range []string{"-1", "1.2", "4294967296", `"0"`, "[]", "true"} {
		cases = append(cases, struct{ name, input, path string }{"average " + value, strings.Replace(minimalJSON, `"AverageRGB":0`, `"AverageRGB":`+value, 1), "SceneInfo[0].LuminanceParameters.AverageRGB"})
	}
	for _, field := range []string{"MaxScl", "LuminanceDistributions"} {
		for _, value := range []string{"1", "[]", `"x"`} {
			if field == "MaxScl" && value == "[]" {
				continue
			}
			cases = append(cases, struct{ name, input, path string }{field + value, strings.Replace(minimalJSON, `"AverageRGB":0`, `"AverageRGB":0,"`+field+`":`+value, 1), field})
		}
	}
	for _, value := range []string{"null", "-1", "1.5", "4294967296", `"2"`} {
		for _, field := range []string{"MaxScl", "DistributionValues"} {
			extra := `"MaxScl":[` + value + `]`
			if field == "DistributionValues" {
				extra = `"LuminanceDistributions":{"DistributionValues":[` + value + `]}`
			}
			cases = append(cases, struct{ name, input, path string }{field + value, strings.Replace(minimalJSON, `"AverageRGB":0`, `"AverageRGB":0,`+extra, 1), field})
		}
	}
	for _, value := range []string{"-1", "0.1", "18446744073709551616", `"1"`} {
		cases = append(cases, struct{ name, input, path string }{"summary " + value, strings.Replace(minimalJSON, `"SceneFrameNumbers":[]`, `"SceneFrameNumbers":[`+value+`]`, 1), "SceneInfoSummary"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metadata, err := DecodeJSON(strings.NewReader(tc.input))
			if metadata != nil || !errors.Is(err, ErrInvalidMetadata) || !strings.Contains(err.Error(), tc.path) {
				t.Fatalf("got %v, %v; expected invalid metadata with %s", metadata, err, tc.path)
			}
		})
	}
	// Full uint32/uint64 ranges are accepted without a 10,000-nit cap.
	input := strings.Replace(minimalJSON, `"AverageRGB":0`, `"AverageRGB":4294967295,"MaxScl":[4294967295]`, 1)
	input = strings.Replace(input, `"SceneFrameNumbers":[]`, `"SceneFrameNumbers":[18446744073709551615]`, 1)
	if _, err := DecodeJSON(strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
}

type brokenReader struct{ err error }

func (r brokenReader) Read([]byte) (int, error) { return 0, r.err }

type ownedReader struct {
	*strings.Reader
	closed bool
}

func (r *ownedReader) Close() error { r.closed = true; return nil }

func TestDecodeJSONErrorAndReaderOwnership(t *testing.T) {
	if m, err := DecodeJSON(nil); m != nil || !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("nil reader: %v, %v", m, err)
	}
	failure := errors.New("reader failure")
	for _, reader := range []io.Reader{brokenReader{failure}, io.MultiReader(strings.NewReader(minimalJSON), brokenReader{failure})} {
		m, err := DecodeJSON(reader)
		if m != nil || !errors.Is(err, failure) || !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("reader identity lost: %v, %v", m, err)
		}
	}
	_, err := DecodeJSON(strings.NewReader(`{"JSONInfo":!}`))
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Fatalf("syntax cause lost: %v", err)
	}
	_, err = DecodeJSON(strings.NewReader(strings.Replace(minimalJSON, `"AverageRGB":0`, `"AverageRGB":-1`, 1)))
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok {
		t.Fatalf("type cause lost: %v", err)
	}
	r := &ownedReader{Reader: strings.NewReader(minimalJSON)}
	if _, err := DecodeJSON(r); err != nil || r.closed {
		t.Fatalf("reader ownership: closed=%v err=%v", r.closed, err)
	}
}

func FuzzDecodeJSON(f *testing.F) {
	for _, input := range []string{minimalJSON, "", "null", minimalJSON + " {}", strings.Replace(minimalJSON, `"AverageRGB":0`, `"AverageRGB":null`, 1)} {
		f.Add([]byte(input))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 64<<10 {
			t.Skip() // Bound fuzz work, not the library's reader policy.
		}
		metadata, err := DecodeJSON(strings.NewReader(string(input)))
		if err != nil {
			if metadata != nil || !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("invalid error contract: %v %v", metadata, err)
			}
		} else if metadata == nil || len(metadata.Frames) == 0 || metadata.SceneCount < 0 {
			t.Fatalf("invalid successful decode: %v", metadata)
		}
	})
}
