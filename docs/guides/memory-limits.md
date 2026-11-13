# Memory limits

Two layers, both checked before the memory they guard is allocated.

## Per call: `Limits`

Zero fields select the defaults; negative fields are `ErrInvalid`. A breach is a
`*LimitError{Name, Got, Max}` wrapping `ErrLimit`. Names are fixed strings; no
input content is echoed.

| Field | Default | Counts |
|---|---|---|
| `PagePixels` | 500 000 000 | page pixels, the pixels of all regions together, and any one symbol's width or height |
| `Symbols` | 1<<20 | symbols per dictionary and per stream |
| `Regions` | 1<<16 | segments per stream |
| `MaxBytes` | 0 (no bound) | the most memory one call may reserve, by the estimate below |
| `Reserved` | false | the caller already holds `MaxBytes` for this call |

`MaxBytes` bounds a call by the library's own estimate. An estimate above it is
a `*LimitError{Name: "MaxBytes"}`, returned before anything is allocated. It is
permanent: the call can never fit. `Reserved` says the caller reserved
`MaxBytes` from its own pool, so the call skips the process-wide pool below and
is bounded by `MaxBytes` alone (the jpeg-go model). `Reserved` without
`MaxBytes`, and a negative `MaxBytes`, are `ErrInvalid`. Both apply to `Decode`
and `Encode`.

Derived from `PagePixels`, and not separately configurable: the page's and all
regions' packed bytes together may not exceed `PagePixels/4`; regions may
cover the page at most 4 times (plus 65 536 pixels); symbol placements are
capped the same way. A stream may declare at most 1<<21 symbol instances.

`Decode` evaluates all of these from the segment headers. Symbol sizes are only
known while decoding (T.88 6.5.5 codes them inside the data), so those are
charged before each symbol is allocated. `Encode` checks `W*H` against
`PagePixels`.

## Process wide: `SetMemoryLimit`

`SetMemoryLimit(n)` caps the bytes all concurrent calls hold between them. A
call reserves its estimated peak (below). If that does not fit what is free now
but would fit alone, the call returns `ErrMemoryLimit` (transient: retry later).
If it is larger than the whole limit it can never fit and the call returns a
`*LimitError{Name: "MemoryLimit"}` (permanent). The reservation is released when the call returns. The default, and
`SetMemoryLimit(0)`, is half of `GOMEMLIMIT` or of the cgroup limit, whichever
is lower, and 1 GiB with neither.

The estimate is computed from the headers (decode) or the bitmap (encode):

| Call | Estimate |
|---|---|
| `Encode` | `6 * len(b.Pix)` + 256 KiB (coder buffer, segment body and stream, each up to 2x by append growth) |
| `Decode` | page and region bitmaps (all of `PagePixels/4` when a dictionary is present, since its symbols are charged as they decode) + 96 bytes per declared symbol + 256 per segment + the joined stream + 256 KiB of context arrays |

`TestEstimateCoversAllocation` asserts that measured `TotalAlloc` never exceeds
the estimate, and `TestRefusesBeforeAllocating` that a refused call allocates
under 1 MiB. The headers themselves are read before the reservation: that costs
at most one descriptor per segment, so `Regions` segments at the default.

## Time

Decode time is linear in decoded pixels and cannot be bounded from the input
length: a blank region codes in a few bytes. `ctx` is checked before every row
(every symbol and every 1024 text instances), so cancellation latency is one row
of work.

## Page size

`PageSize(data, globals)` reads the page-information segment's width and height
from the segment headers, allocating nothing proportional to the page, so a
caller can budget a decode first. An unknown height (striped page), no
page-information segment, or malformed headers return an error wrapping
`ErrNoPageSize`.
