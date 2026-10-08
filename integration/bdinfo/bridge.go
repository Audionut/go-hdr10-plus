// SPDX-License-Identifier: GPL-2.0-or-later

// Package bdinfo collects HDR10+ during the local issue-45 BDInfo scan. This
// development module requires the unreleased public video/timeline API.
package bdinfo

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"

	hdr10plus "github.com/Audionut/go-hdr10-plus"
	"github.com/Audionut/go-hdr10-plus/extract"
	scanner "github.com/autobrr/go-bdinfo/pkg/bdinfo"
	"github.com/autobrr/go-bdinfo/pkg/bdinfo/video"
)

// Options configures one report-plus-extraction scan. The caller owns the
// scanner's source/report settings. An existing VideoConsumer is invalid.
type Options struct {
	BDInfo               scanner.Options
	Limits               extract.Limits
	MaxScanRetainedBytes int64
}

// SourceError identifies collection failure independently of the report.
type SourceError struct {
	Key extract.StreamKey
	Err error
}

// PlaylistResult is complete or carries an explicit assembly failure.
type PlaylistResult struct {
	Name       string
	Extraction *extract.Extraction
	Err        error
}

// Outcome preserves the report and separately exposes metadata outcomes.
// DeclinedAfterExhaustion is aggregate; ExhaustedSources has at most 16 samples.
type Outcome struct {
	Report                  *scanner.Result
	Playlists               []PlaylistResult
	SourceErrors            []SourceError
	DeclinedAfterExhaustion uint64
	ExhaustedSources        []extract.StreamKey
	// OmittedPlaylistResults counts outcomes that could not be retained under
	// the shared quota. ExhaustedPlaylists has at most 16 name samples.
	OmittedPlaylistResults uint64
	ExhaustedPlaylists     []string
	// PeakRetainedBytes measures extraction's accounted storage, not process RSS.
	PeakRetainedBytes int64
}

type session struct {
	mu         sync.Mutex
	ctx        context.Context
	options    Options
	budget     *extract.MemoryBudget
	collectors map[extract.StreamKey]*consumer
	exhausted  bool
	declined   uint64
	samples    []extract.StreamKey
}
type consumer struct {
	session *session
	key     extract.StreamKey
	stream  *extract.Stream
	clip    *extract.Clip
	err     error
	esEnd   uint64
}

func (s *session) key(info video.StreamInfo) extract.StreamKey {
	return extract.StreamKey{SourceID: s.options.BDInfo.Path, ClipID: info.Source.Path, TrackID: uint64(info.PID)}
}

func (s *session) factory(ctx context.Context, info video.StreamInfo) (video.Consumer, error) {
	if info.Codec != video.HEVC {
		return nil, nil
	}
	primary := false
	for _, o := range info.Occurrences {
		if o.Mapping.Role == video.Primary && o.Mapping.EntryType == 1 {
			primary = true
			break
		}
	}
	if !primary {
		return nil, nil
	}
	key := s.key(info)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.collectors[key]; ok {
		return nil, fmt.Errorf("duplicate physical collector: %w", extract.ErrInvalidBitstream)
	}
	if s.exhausted {
		s.declined++
		if len(s.samples) < 16 {
			key.SourceID = strings.Clone(key.SourceID[:min(len(key.SourceID), 128)])
			key.ClipID = strings.Clone(key.ClipID[:min(len(key.ClipID), 128)])
			s.samples = append(s.samples, key)
		}
		return nil, extract.ErrResourceLimit
	}
	charge, err := s.budget.Reserve(2048 + int64(len(key.SourceID)+len(key.ClipID)))
	if err != nil {
		s.exhausted = errors.Is(err, extract.ErrResourceLimit)
		s.declined++
		return nil, err
	}
	key.SourceID = strings.Clone(key.SourceID)
	key.ClipID = strings.Clone(key.ClipID)
	c := &consumer{session: s, key: key}
	s.collectors[key] = c
	c.stream, c.err = extract.NewStream(extract.StreamOptions{Key: key, RetainBoundarySyntax: true, Limits: s.options.Limits, Budget: s.budget})
	if c.err != nil {
		if errors.Is(c.err, extract.ErrResourceLimit) {
			s.exhausted = true
		}
		return nil, c.err
	}
	// Registered outcome/map storage stays charged for the complete scan session.
	_ = charge
	return c, nil
}

func (c *consumer) fail(err error) error {
	if c.err == nil {
		c.err = err
		c.stream.Abort(err)
	}
	if errors.Is(err, extract.ErrResourceLimit) {
		c.session.mu.Lock()
		c.session.exhausted = true
		c.session.mu.Unlock()
	}
	return c.err
}

func (c *consumer) Consume(ctx context.Context, chunk video.Chunk) error {
	if c.err != nil {
		return c.err
	}
	if uint64(len(chunk.Starts)) > uint64(math.MaxInt64/144) {
		return c.fail(extract.ErrResourceLimit)
	}
	charge, err := c.session.budget.Reserve(int64(len(chunk.Starts)) * 144)
	if err != nil {
		return c.fail(err)
	}
	defer charge.Release()
	markers := make([]extract.Marker, len(chunk.Starts))
	for i, p := range chunk.Starts {
		if len(p.Header) == 0 {
			return c.fail(fmt.Errorf("missing original PES header: %w", extract.ErrUnsupportedInput))
		}
		if p.Segment != 0 || p.PTS >= 1<<33 || p.DTS >= 1<<33 {
			return c.fail(extract.ErrIncomplete)
		}
		markers[i] = extract.Marker{ESOffset: p.Offset, Kind: extract.PESStart, Modulo33: true, PacketIndex: p.PacketIndex, SourceOffset: p.SourceOffset, HasSourcePosition: true, PESHeader: p.Header, PTS: extract.Timestamp{Value: int64(p.PTS), Timescale: 90000, Valid: p.HasPTS}, DTS: extract.Timestamp{Value: int64(p.DTS), Timescale: 90000, Valid: p.HasDTS}}
	}
	if err := c.stream.Push(ctx, extract.Chunk{ESOffset: chunk.Offset, Data: chunk.Data, Markers: markers}); err != nil {
		return c.fail(err)
	}
	c.esEnd = chunk.Offset + uint64(len(chunk.Data))
	return nil
}

func (c *consumer) Discontinuity(ctx context.Context, event video.Discontinuity) error {
	return c.fail(fmt.Errorf("transport discontinuity at %d: %w", event.Offset, extract.ErrIncomplete))
}

func (c *consumer) Finish(end video.End) error {
	if c.err != nil {
		return c.err
	}
	if !end.CleanEOF {
		cause := end.Err
		if cause == nil {
			cause = extract.ErrIncomplete
		}
		return c.fail(cause)
	}
	if end.SourcePacketCount == 0 {
		return c.fail(fmt.Errorf("missing observed source extent: %w", extract.ErrUnsupportedInput))
	}
	marker := [1]extract.Marker{{ESOffset: c.esEnd, Kind: extract.SourceEnd, PacketIndex: end.SourcePacketCount, HasSourcePosition: true}}
	if err := c.stream.Push(c.session.ctx, extract.Chunk{ESOffset: c.esEnd, Markers: marker[:]}); err != nil {
		return c.fail(err)
	}
	var err error
	c.clip, err = c.stream.Finish(c.session.ctx)
	if err != nil {
		return c.fail(err)
	}
	return nil
}

// Scan performs one BDInfo Run and uses only its synchronous delivered bytes.
// Extraction failures remain in Outcome when ordinary reporting succeeds.
// Verified seamless joins are still gated on the core's splice acceptance suite.
func Scan(ctx context.Context, options Options) (*Outcome, error) {
	if ctx == nil || options.MaxScanRetainedBytes < 0 || options.BDInfo.VideoConsumer != nil {
		return nil, fmt.Errorf("%w: bridge options", hdr10plus.ErrInvalidOptions)
	}
	validation, err := extract.NewStream(extract.StreamOptions{Limits: options.Limits})
	if err != nil {
		return nil, err
	}
	validation.Abort(nil)
	maxBytes := options.MaxScanRetainedBytes
	if maxBytes == 0 {
		maxBytes = 1 << 30
	}
	budget, err := extract.NewMemoryBudget(maxBytes)
	if err != nil {
		return nil, err
	}
	if _, err := budget.Reserve(16384); err != nil {
		return nil, err
	}
	s := &session{ctx: ctx, options: options, budget: budget, collectors: make(map[extract.StreamKey]*consumer), samples: make([]extract.StreamKey, 0, 16)}
	options.BDInfo.VideoConsumer = s.factory
	report, reportErr := scanner.Run(ctx, options.BDInfo)
	out := &Outcome{Report: &report, DeclinedAfterExhaustion: s.declined, ExhaustedSources: s.samples, Playlists: make([]PlaylistResult, 0, 16), ExhaustedPlaylists: make([]string, 0, 16), SourceErrors: make([]SourceError, 0, len(s.collectors))}
	for key, c := range s.collectors {
		if c.err != nil {
			out.SourceErrors = append(out.SourceErrors, SourceError{Key: key, Err: c.err})
		}
	}
	var resultBacking *extract.Reservation
	for _, timeline := range report.Timelines {
		// The first 16 outcomes and exhaustion names use the initial failure
		// reservation. Later growth and error slots remain charged until return.
		if len(out.Playlists) == cap(out.Playlists) {
			capacity := 2 * cap(out.Playlists)
			backing, err := budget.Reserve(int64(capacity) * 64)
			var slots *extract.Reservation
			if err == nil {
				slots, err = budget.Reserve(int64(capacity-cap(out.Playlists)) * 512)
			}
			if err != nil {
				backing.Release()
				slots.Release()
				out.OmittedPlaylistResults++
				if len(out.ExhaustedPlaylists) < 16 {
					name := timeline.Name
					out.ExhaustedPlaylists = append(out.ExhaustedPlaylists, strings.Clone(name[:min(128, len(name))]))
				}
				continue
			}
			buffer := make([]PlaylistResult, len(out.Playlists), capacity)
			copy(buffer, out.Playlists)
			out.Playlists = buffer
			resultBacking.Release()
			resultBacking = backing
		}
		result := PlaylistResult{Name: timeline.Name}
		charge, err := budget.Reserve(int64(len(timeline.Items)) * 1024)
		var playlist extract.Playlist
		var clips map[extract.StreamKey]*extract.Clip
		if err == nil {
			playlist, clips, err = s.playlist(timeline)
		}
		if err == nil {
			result.Extraction, err = extract.Assemble(ctx, playlist, clips, extract.AssemblyOptions{Limits: options.Limits, Budget: budget})
		}
		charge.Release()
		result.Err = err
		out.Playlists = append(out.Playlists, result)
	}
	_, out.PeakRetainedBytes = budget.Usage()
	return out, reportErr
}

func (s *session) playlist(t scanner.PlaylistTimeline) (extract.Playlist, map[extract.StreamKey]*extract.Clip, error) {
	p := extract.Playlist{ID: t.Name}
	clips := make(map[extract.StreamKey]*extract.Clip)
	if !t.Complete || !t.Scanned || t.Err != nil {
		return p, nil, fmt.Errorf("timeline %s: %w", t.Name, errors.Join(extract.ErrIncomplete, t.Err))
	}
	for _, item := range t.Items {
		if item.Err != nil || item.SelectedAlternative < 0 || item.SelectedAlternative >= len(item.Angles) || item.Offset45 > math.MaxInt64 {
			return p, nil, extract.ErrUnsupportedInput
		}
		angle := item.Angles[item.SelectedAlternative]
		if angle.Err != nil || angle.STC == nil || !angle.STC.HasEndPacket || angle.STC.EndPacket <= uint64(angle.STC.StartPacket) || angle.Source.Kind != "m2ts" || (item.ConnectionCondition != 1 && item.ConnectionCondition != 5 && item.ConnectionCondition != 6) || angle.Index < 0 || uint64(angle.Index) > math.MaxUint32 || item.In45 < angle.STC.PresentationStart45 || item.Out45 > angle.STC.PresentationEnd45 {
			return p, nil, fmt.Errorf("unverified clock/splice: %w", extract.ErrUnsupportedInput)
		}
		var pid uint16
		for _, mapping := range angle.Video {
			if mapping.Role == video.Primary && mapping.Codec == video.HEVC && mapping.EntryType == 1 {
				if pid != 0 {
					return p, nil, extract.ErrAmbiguousTrack
				}
				pid = mapping.PID
			}
		}
		if pid == 0 {
			return p, nil, extract.ErrUnsupportedInput
		}
		key := extract.StreamKey{SourceID: s.options.BDInfo.Path, ClipID: angle.Source.Path, TrackID: uint64(pid)}
		c := s.collectors[key]
		if c == nil && s.exhausted {
			return p, nil, extract.ErrResourceLimit
		}
		if c != nil && c.err != nil {
			return p, nil, c.err
		}
		if c == nil || c.clip == nil {
			return p, nil, extract.ErrIncomplete
		}
		clips[key] = c.clip
		connection := extract.ConnectionReset
		if len(p.Occurrences) > 0 && item.ConnectionCondition != 1 {
			connection = extract.ConnectionSeamless
		}
		p.Occurrences = append(p.Occurrences, extract.Occurrence{Key: key, Angle: uint32(angle.Index), In: int64(item.In45), Out: int64(item.Out45), PlaylistStart: int64(item.Offset45), ClockOffset: extract.Timestamp{Timescale: 1, Valid: true}, Connection: connection, Packets: &extract.PacketRange{Start: uint64(angle.STC.StartPacket), End: angle.STC.EndPacket, ClockAnchor: extract.Timestamp{Value: int64(angle.STC.PresentationStart45) * 2, Timescale: 90000, Valid: true}}})
	}
	return p, clips, nil
}
