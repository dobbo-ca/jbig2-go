package jbig2

import (
	"context"
	"errors"
	"fmt"
)

// Limits are the per-call bounds the budget rules in segment_decode.go are
// evaluated against. The root package resolves zero values to its defaults;
// nothing here does.
type Limits struct {
	PagePixels int64 // rules 1-3: page and region pixels; rule 4 is PagePixels/4 bytes
	Regions    int64 // rule 5: segments per stream
	Symbols    int64 // rule 6: symbols per dictionary and per stream
}

// legacy is the budget this code was written against (byblos) and the one the
// package-level wrappers and the in-package tests run under.
var legacy = Limits{PagePixels: MaxPagePixels, Regions: maxStreamSegments, Symbols: maxStreamSymbols}

// bitmapBytes is rule 4's cap: the page's and every region's packed bytes.
func (l Limits) bitmapBytes() int64 { return l.PagePixels / 4 }

// ErrLimit is wrapped by every *LimitError.
var ErrLimit = errors.New("jbig2: limit exceeded")

// LimitError reports which cap tripped and by how much. Name is a fixed
// string; no input content is ever echoed.
type LimitError struct {
	Name     string
	Got, Max int64
	msg      string
}

func (e *LimitError) Error() string {
	if e.msg == "" {
		return fmt.Sprintf("jbig2: limit exceeded: %s %d > %d", e.Name, e.Got, e.Max)
	}
	return e.msg
}
func (e *LimitError) Unwrap() error { return ErrLimit }

func limitErr(name string, got, max int64, format string, a ...any) error {
	return &LimitError{Name: name, Got: got, Max: max, msg: fmt.Sprintf(format, a...)}
}

// estimate is the decode's peak allocation, from the headers alone: every
// bitmap the budget admits (rule 4's running total, widened to its cap when a
// dictionary may still grow it), a Bitmap header, slice slot and pixel slab per
// declared symbol, a descriptor per segment, the joined stream, and the
// context arrays of one region and one dictionary.
func (p streamPlan) estimate(streamLen int) int64 {
	bm := p.budget.bmBytes
	if p.symbolsDeclared > 0 {
		bm = p.budget.lim.bitmapBytes()
	}
	return bm + 96*p.symbolsDeclared + 256*int64(len(p.work)) + int64(streamLen) + 4<<16
}

// ctxErr is nil for a nil context, which only in-package tests pass.
func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
