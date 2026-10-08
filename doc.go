// Package hdr10plus decodes HDR10+ plotting metadata and renders brightness
// charts entirely in Go. Frame luminance values use units of 0.1 nit.
//
// The numerical behavior follows quietvoid/hdr10plus_tool at revision
// 82cb01ba387184476dbf5fa3bddebf0a345693d7, including means calculated in PQ
// space. Embedded fonts and x-axis tick placement deliberately differ.
//
// The API is synchronous: applications own reader limits, deadlines, PNG
// encoding, and output files. It does not perform full HDR10+ schema or profile
// validation. Metadata is a plotting model, not a JSON round-trip model.
package hdr10plus
