// Package extract collects raw Annex B and length-prefixed HEVC syntax, decodes
// the single-window, application-version-1 HDR10+ registered T.35 dialect, and
// projects resolved picture metadata into the parent package's plotting model.
// Luminance stays in encoded 0.1-nit units. File extraction accepts raw Annex B,
// Matroska, ordinary/fragmented MP4, TS and M2TS in the documented supported scope.
//
// A successful result covers the structurally identified output pictures through
// delivered EOF. It does not certify entropy-coded picture integrity or prove
// that an otherwise indistinguishable raw VCL-tail cut is an original full file.
package extract
