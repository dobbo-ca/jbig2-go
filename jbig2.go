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
// anything is allocated, and a context checked between rows. See
// docs/guides/memory-limits.md.
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
	// estimated peak does not fit what SetMemoryLimit leaves free.
	ErrMemoryLimit = errors.New("jbig2: memory limit exceeded")
	// ErrInvalid marks a negative limit or a malformed Bitmap.
	ErrInvalid = errors.New("jbig2: invalid argument")
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
}

func (l Limits) resolve() (ij.Limits, error) {
	if l.PagePixels < 0 || l.Symbols < 0 || l.Regions < 0 {
		return ij.Limits{}, fmt.Errorf("%w: negative limit", ErrInvalid)
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
	release, err := reserve(encodeEstimate(b))
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
	return ij.DecodeStream(ctx, globals, data, l, reserve)
}
