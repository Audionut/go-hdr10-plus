package extract

// Wire syntax and supported percentile validation are informed by
// quietvoid/hdr10plus_tool 82cb01b (MIT). See THIRD_PARTY_NOTICES.md.
import (
	"bytes"
	"fmt"
	"slices"

	hdr10plus "github.com/Audionut/go-hdr10-plus"
)

// Distribution retains a percentage index and its encoded 0.1-nit value in wire order.
type Distribution struct {
	Index uint8
	Value uint32
}

// Curve holds encoded knee coordinates (0..4095) and anchors (0..1023), not nits.
type Curve struct {
	KneeX, KneeY uint16
	Anchors      []uint16
}

// Payload retains every field of the supported one-window dialect. Unsupported
// grid and saturation flags are retained explicitly and must be false.
type Payload struct {
	ApplicationVersion           uint8
	NumWindows                   uint8
	TargetMaximumLuminance       uint32 // nits, unlike MaxSCL and AverageRGB
	TargetActualPeakLuminance    bool
	MaxSCL                       [3]uint32
	AverageRGB                   uint32
	Distributions                []Distribution
	FractionBrightPixels         uint16
	MasteringActualPeakLuminance bool
	ToneMapping                  bool
	Curve                        *Curve
	ColorSaturationMapping       bool
}

var t35Identity = []byte{0xb5, 0, 0x3c, 0, 1, 4}
var indexes9 = []uint8{1, 5, 10, 25, 50, 75, 90, 95, 99}
var indexes10 = []uint8{1, 5, 10, 25, 50, 75, 90, 95, 98, 99}

// DecodeT35 decodes a complete registered payload including its six identity
// bytes, excluding the SEI envelope and RBSP trailing bits. Failed calls return
// nil. It consumes exactly the supported syntax and zero alignment padding.
// It does not borrow the input or validate compressed video pictures.
func DecodeT35(data []byte) (*Payload, error) {
	for i := range min(len(data), len(t35Identity)) {
		if data[i] != t35Identity[i] {
			return nil, fieldError("registered-identity", ErrUnsupportedMetadata)
		}
	}
	if len(data) < len(t35Identity)+1 {
		return nil, fieldError("registered-identity", hdr10plus.ErrInvalidMetadata)
	}
	b := bitReader{data: data[6:]}
	p := &Payload{ApplicationVersion: uint8(b.read(8))}
	if p.ApplicationVersion != 1 {
		return nil, fieldError("application-version", ErrUnsupportedMetadata)
	}
	p.NumWindows = uint8(b.read(2))
	if b.err != nil {
		return nil, fieldError("num-windows", fmt.Errorf("%w: %w", hdr10plus.ErrInvalidMetadata, b.err))
	}
	if p.NumWindows == 0 {
		return nil, fieldError("num-windows", hdr10plus.ErrInvalidMetadata)
	}
	if p.NumWindows != 1 {
		return nil, fieldError("num-windows", ErrUnsupportedMetadata)
	}
	p.TargetMaximumLuminance = uint32(b.read(27))
	p.TargetActualPeakLuminance = b.flag()
	if p.TargetActualPeakLuminance {
		return nil, fieldError("target-actual-peak-grid", ErrUnsupportedMetadata)
	}
	for i := range p.MaxSCL {
		p.MaxSCL[i] = uint32(b.read(17))
	}
	p.AverageRGB = uint32(b.read(17))
	count := int(b.read(4))
	if b.err != nil {
		return nil, fieldError("luminance", fmt.Errorf("%w: %w", hdr10plus.ErrInvalidMetadata, b.err))
	}
	if count != 9 && count != 10 {
		return nil, fieldError("distribution-count", hdr10plus.ErrInvalidMetadata)
	}
	p.Distributions = make([]Distribution, count)
	for i := range p.Distributions {
		p.Distributions[i] = Distribution{uint8(b.read(7)), uint32(b.read(17))}
	}
	p.FractionBrightPixels = uint16(b.read(10))
	p.MasteringActualPeakLuminance = b.flag()
	if p.MasteringActualPeakLuminance {
		return nil, fieldError("mastering-actual-peak-grid", ErrUnsupportedMetadata)
	}
	p.ToneMapping = b.flag()
	if p.ToneMapping {
		p.Curve = &Curve{KneeX: uint16(b.read(12)), KneeY: uint16(b.read(12))}
		count = int(b.read(4))
		if count > 9 {
			return nil, fieldError("anchor-count", hdr10plus.ErrInvalidMetadata)
		}
		p.Curve.Anchors = make([]uint16, count)
		for i := range p.Curve.Anchors {
			p.Curve.Anchors[i] = uint16(b.read(10))
		}
	}
	p.ColorSaturationMapping = b.flag()
	if p.ColorSaturationMapping {
		return nil, fieldError("color-saturation-mapping", ErrUnsupportedMetadata)
	}
	if b.err != nil {
		return nil, fieldError("payload-syntax", fmt.Errorf("%w: %w", hdr10plus.ErrInvalidMetadata, b.err))
	}
	padding := (8 - b.pos%8) % 8
	if len(b.data)*8-b.pos != padding || b.read(uint(padding)) != 0 {
		return nil, fieldError("payload-padding", hdr10plus.ErrInvalidMetadata)
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Payload) validate() error {
	bad := func(field string) error { return fieldError(field, hdr10plus.ErrInvalidMetadata) }
	if p.ApplicationVersion != 1 || p.NumWindows != 1 || p.TargetActualPeakLuminance || p.MasteringActualPeakLuminance || p.ColorSaturationMapping {
		return bad("supported-dialect")
	}
	if p.TargetMaximumLuminance > 10000 {
		return bad("target-maximum-luminance")
	}
	for _, v := range p.MaxSCL {
		if v > 100000 {
			return bad("max-scl")
		}
	}
	if p.AverageRGB > 100000 || p.FractionBrightPixels > 1023 {
		return bad("luminance")
	}
	indexes := indexes9
	if len(p.Distributions) == 10 {
		indexes = indexes10
	} else if len(p.Distributions) != 9 {
		return bad("distribution-count")
	}
	for i, d := range p.Distributions {
		if d.Index != indexes[i] || d.Value > 100000 {
			return bad("distribution")
		}
	}
	if p.ToneMapping != (p.Curve != nil) {
		return bad("tone-mapping-curve")
	}
	if p.Curve != nil {
		if p.Curve.KneeX > 4095 || p.Curve.KneeY > 4095 || len(p.Curve.Anchors) > 9 {
			return bad("curve")
		}
		for _, a := range p.Curve.Anchors {
			if a > 1023 {
				return bad("curve-anchor")
			}
		}
	}
	return nil
}

func (p Payload) profile() string {
	if !p.ToneMapping && p.TargetMaximumLuminance == 0 {
		return "A"
	}
	if p.ToneMapping && p.TargetMaximumLuminance != 0 && p.Curve != nil && len(p.Curve.Anchors) != 0 {
		return "B"
	}
	return "N/A"
}

func curveEqual(a, b *Curve) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.KneeX == b.KneeX && a.KneeY == b.KneeY && slices.Equal(a.Anchors, b.Anchors)
}

func sceneEqual(a, b Payload) bool {
	return a.NumWindows == b.NumWindows && a.TargetMaximumLuminance == b.TargetMaximumLuminance && a.MaxSCL == b.MaxSCL && a.AverageRGB == b.AverageRGB && slices.Equal(a.Distributions, b.Distributions) && a.ToneMapping == b.ToneMapping && curveEqual(a.Curve, b.Curve)
}

func payloadEqual(a, b Payload) bool {
	return sceneEqual(a, b) && a.ApplicationVersion == b.ApplicationVersion && a.FractionBrightPixels == b.FractionBrightPixels && a.TargetActualPeakLuminance == b.TargetActualPeakLuminance && a.MasteringActualPeakLuminance == b.MasteringActualPeakLuminance && a.ColorSaturationMapping == b.ColorSaturationMapping
}

func clonePayload(p Payload) Payload {
	p.Distributions = slices.Clone(p.Distributions)
	if p.Curve != nil {
		c := *p.Curve
		c.Anchors = slices.Clone(c.Anchors)
		p.Curve = &c
	}
	return p
}

// matchesT35 also recognizes a truncated matching identity, so malformed
// HDR10+ cannot disappear as an unrelated registered payload.
func matchesT35(data []byte) bool {
	return bytes.Equal(data[:min(len(data), 6)], t35Identity[:min(len(data), 6)])
}
