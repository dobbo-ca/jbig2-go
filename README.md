# jbig2-go

Lossless JBIG2 (ITU-T T.88) encoder and decoder in pure Go. No cgo, no lossy
mode of any kind, no assembly.

```go
data, err := jbig2.Encode(ctx, bitmap, jbig2.Limits{})
page, err := jbig2.Decode(ctx, data, globals, jbig2.Limits{})
```

The streams are the embedded organization the PDF `JBIG2Decode` filter uses
(ISO 32000-1:2008 7.4.7). Encode writes a generic region (template 0, nominal AT
pixels, TPGDON). Decode reads generic regions (all templates, any AT pixels) and
arithmetic symbol dictionaries and text regions. Huffman, MMR, refinement and
halftone coding return `ErrUnsupportedFeature`.

Every call takes `Limits` and a context; see
[docs/guides/memory-limits.md](docs/guides/memory-limits.md).

MIT licensed. The code moved here from github.com/dobbo-ca/byblos, whose
history for it is kept.
