// Package jbig2 is a lossless JBIG2 (ITU-T T.88) encoder and decoder in pure
// Go, for the embedded stream organization the PDF JBIG2Decode filter uses
// (ISO 32000-1:2008 7.4.7). There is no lossy option of any kind.
//
// Encode writes one page as a single generic region (GBTEMPLATE 0, nominal AT
// pixels, TPGDON). Decode reads generic regions (all four templates, any AT
// pixels), and the arithmetic symbol dictionary and text region procedures.
// Huffman, MMR, refinement and halftone coding are reported as
// ErrUnsupportedFeature before any coded bit is read.
//
// Every entry point takes Limits, checked against the stream's headers before
// anything is allocated, and a context checked between rows. Memory comes
// from a process-wide pool (SetMemoryLimit), or, with Limits.Reserved, from
// the caller's own reservation bounded by Limits.MaxBytes. A request that can
// never fit is a permanent *LimitError; ErrMemoryLimit means it would fit
// alone but not now, and is transient. See docs/guides/memory-limits.md.
package jbig2

import (
	"context"
	"errors"
	"fmt"

	ij "github.com/dobbo-ca/jbig2-go/internal/jbig2"
)

// Bitmap is a 1-bit-per-pixel bitmap, MSB first, a set bit being ink.
type Bitmap = ij.Bitmap

// NewBitmap returns an all-background bitmap; it panics on non-positive dimensions.
func NewBitmap(w, h int) *Bitmap { return ij.NewBitmap(w, h) }

// LimitError names the cap that tripped; it wraps ErrLimit.
type LimitError = ij.LimitError

var (
	// ErrLimit is wrapped by every *LimitError.
	ErrLimit = ij.ErrLimit
	// ErrUnsupportedFeature marks a legal stream this package does not decode.
	ErrUnsupportedFeature = ij.ErrUnsupportedFeature
	// ErrMemoryLimit is returned, before anything is allocated, when a call's
	// estimated peak would fit the pool alone but not what SetMemoryLimit
	// leaves free now: retrying later can succeed. A call that could never
	// fit is a *LimitError named "MemoryLimit" or "MaxBytes" instead.
	ErrMemoryLimit = errors.New("jbig2: memory limit exceeded")
	// ErrInvalid marks a negative limit, Reserved without MaxBytes, or a malformed Bitmap.
	ErrInvalid = errors.New("jbig2: invalid argument")
	// ErrNoPageSize is wrapped by PageSize when the headers do not give a page size.
	ErrNoPageSize = errors.New("jbig2: no page size in headers")
)

// Defaults for the zero fields of Limits.
const (
	DefaultPagePixels = 500_000_000
	DefaultSymbols    = 1 << 20
	DefaultRegions    = 1 << 16
)

// Limits bound one call. A zero field selects its default; a negative one is
// ErrInvalid. A breach is a *LimitError, returned before the memory is allocated.
type Limits struct {
	PagePixels int64 // page and region pixels (default DefaultPagePixels)
	Symbols    int64 // symbols per dictionary and per stream (default DefaultSymbols)
	Regions    int64 // segments per stream (default DefaultRegions)

	// MaxBytes is the most memory one call may reserve, by the library's own
	// estimate (see docs/guides/memory-limits.md). 0 is no extra bound. An
	// estimate above it is a *LimitError named "MaxBytes", returned before
	// anything is allocated; it is permanent, the call can never fit.
	MaxBytes int64
	// Reserved says the caller already holds MaxBytes for this call from its
	// own pool. The call then skips the package pool and is bounded by
	// MaxBytes alone. It needs MaxBytes > 0, else ErrInvalid.
	Reserved bool
}

// reserver is the per-call reservation hook: MaxBytes bounds what the call
// reserves in total, and unless Reserved the package pool is charged too.
func (l Limits) reserver() func(int64) (func(), error) {
	var used int64
	return func(n int64) (func(), error) {
		if l.MaxBytes > 0 && used+n > l.MaxBytes {
			return nil, &LimitError{Name: "MaxBytes", Got: used + n, Max: l.MaxBytes}
		}
		release := func() {}
		if !l.Reserved {
			var err error
			if release, err = reserve(n); err != nil {
				return nil, err
			}
		}
		used += n
		return func() { used -= n; release() }, nil
	}
}

func (l Limits) resolve() (ij.Limits, error) {
	if l.PagePixels < 0 || l.Symbols < 0 || l.Regions < 0 || l.MaxBytes < 0 {
		return ij.Limits{}, fmt.Errorf("%w: negative limit", ErrInvalid)
	}
	if l.Reserved && l.MaxBytes == 0 {
		return ij.Limits{}, fmt.Errorf("%w: Reserved needs MaxBytes", ErrInvalid)
	}
	r := ij.Limits{PagePixels: l.PagePixels, Symbols: l.Symbols, Regions: l.Regions}
	if r.PagePixels == 0 {
		r.PagePixels = DefaultPagePixels
	}
	if r.Symbols == 0 {
		r.Symbols = DefaultSymbols
	}
	if r.Regions == 0 {
		r.Regions = DefaultRegions
	}
	return r, nil
}

// Impl reports the kernel in use. There is no assembly in this package, on
// either architecture.
func Impl() string { return "generic" }

// encodeEstimate is Encode's peak allocation: the coder output (at worst a bit
// over the packed input) is held in its buffer, in the segment body and in the
// stream, and append growth can double each.
func encodeEstimate(b *Bitmap) int64 { return 6*int64(len(b.Pix)) + 4<<16 }

// Encode returns b as a complete embedded JBIG2 stream. It zeroes b's padding
// bits (those past W in each row), which are not pixels.
func Encode(ctx context.Context, b *Bitmap, lim Limits) ([]byte, error) {
	l, err := lim.resolve()
	if err != nil {
		return nil, err
	}
	if b == nil || b.W <= 0 || b.H <= 0 || b.Stride < (b.W+7)/8 || len(b.Pix) < b.Stride*b.H {
		return nil, fmt.Errorf("%w: bitmap", ErrInvalid)
	}
	if px := int64(b.W) * int64(b.H); px > l.PagePixels {
		return nil, &LimitError{Name: "PagePixels", Got: px, Max: l.PagePixels}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	release, err := lim.reserver()(encodeEstimate(b))
	if err != nil {
		return nil, err
	}
	defer release()
	return ij.EncodeStream(ctx, b)
}

// Decode returns the page in an embedded JBIG2 stream. globals is the page-0
// stream a PDF /JBIG2Globals object carries, or nil.
func Decode(ctx context.Context, data, globals []byte, lim Limits) (*Bitmap, error) {
	l, err := lim.resolve()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return ij.DecodeStream(ctx, globals, data, l, lim.reserver())
}

// PageSize returns the page width and height from the page-information segment,
// reading segment headers only: nothing proportional to the page is allocated.
// globals is the /JBIG2Globals stream or nil. An unknown height (a striped
// page), a missing page-information segment or malformed headers return an
// error wrapping ErrNoPageSize.
func PageSize(data, globals []byte) (w, h int, err error) {
	w, h, err = ij.PageInfoSize(globals, data)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %w", ErrNoPageSize, err)
	}
	return w, h, nil
}
