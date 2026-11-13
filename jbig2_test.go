package jbig2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	ij "github.com/dobbo-ca/jbig2-go/internal/jbig2"
)

// noise fills a bitmap from a fixed xorshift stream, so every run and every
// architecture sees the same pixels. ink is the 1-in-n chance of a set pixel.
func noise(w, h, stride int, seed uint64, n uint64) *Bitmap {
	b := NewBitmap(w, h)
	if stride > b.Stride {
		b.Stride, b.Pix = stride, make([]byte, stride*h)
	}
	x := seed
	for y := range h {
		for c := range w {
			x ^= x << 13
			x ^= x >> 7
			x ^= x << 17
			if x%n == 0 {
				b.Set(c, y, 1)
			}
		}
	}
	return b
}

func same(a, b *Bitmap) bool {
	if a.W != b.W || a.H != b.H {
		return false
	}
	for y := range a.H {
		for x := range a.W {
			if a.Get(x, y) != b.Get(x, y) {
				return false
			}
		}
	}
	return true
}

func TestRoundTrip(t *testing.T) {
	for _, w := range []int{1, 15, 16, 17, 31, 32, 33, 63, 64, 65} {
		for _, h := range []int{1, 2, 7, 40} {
			for _, extra := range []int{0, 3} { // odd strides: rows wider than (w+7)/8
				b := noise(w, h, (w+7)/8+extra, uint64(w*131+h), 3)
				want := &Bitmap{W: w, H: h, Stride: b.Stride, Pix: bytes.Clone(b.Pix)}
				for y := range h { // dirty padding must not leak into the code
					for i := (w - 1) / 8; i < b.Stride; i++ {
						if i > (w-1)/8 || w%8 != 0 {
							b.Pix[y*b.Stride+i] |= 0x01
						}
					}
				}
				data, err := Encode(context.Background(), b, Limits{})
				if err != nil {
					t.Fatalf("%dx%d stride+%d: Encode: %v", w, h, extra, err)
				}
				got, err := Decode(context.Background(), data, nil, Limits{})
				if err != nil {
					t.Fatalf("%dx%d stride+%d: Decode: %v", w, h, extra, err)
				}
				if !same(want, got) {
					t.Fatalf("%dx%d stride+%d: decode(encode(x)) != x", w, h, extra)
				}
			}
		}
	}
}

// Hash-exact on every architecture: the coder is integer-only Go.
func TestGolden(t *testing.T) {
	for _, c := range []struct {
		w, h int
		n    uint64
		want string
	}{
		{1, 1, 2, "b0b24d815607f6ed6c397e0b3e9be114f36776499e8b76eafe36a642b4d8aba1"},
		{33, 17, 3, "150bcb907b35a79dfe0b60c22249f9bbe86b00ac9c9bfd7bbf03c19e5b15a8b7"},
		{257, 129, 5, "798bc1f671ab239219b83c13e3abe5342dbb0714ee5be628de5cbdf2b11b1622"},
		{1000, 1000, 40, "16706dccbdb659214d69ee96d44c68de88b6421c9e5cb0512c54bef8dedd8aa6"},
	} {
		data, err := Encode(context.Background(), noise(c.w, c.h, 0, 99, c.n), Limits{})
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != c.want {
			t.Errorf("%dx%d: sha256 = %s; want %s", c.w, c.h, got, c.want)
		}
	}
}

func seg(num uint32, typ byte, data []byte) []byte {
	h := binary.BigEndian.AppendUint32(nil, num)
	h = append(h, typ, 0, 1)
	h = binary.BigEndian.AppendUint32(h, uint32(len(data)))
	return append(h, data...)
}

func pageInfo(w, h uint32) []byte {
	d := binary.BigEndian.AppendUint32(nil, w)
	d = binary.BigEndian.AppendUint32(d, h)
	return append(d, make([]byte, 11)...)
}

// region is an immediate generic region at (0,0): template 0, nominal AT, no TPGDON.
func region(w, h uint32, coded []byte) []byte {
	d := binary.BigEndian.AppendUint32(nil, w)
	d = binary.BigEndian.AppendUint32(d, h)
	d = append(d, make([]byte, 9)...)
	d = append(d, 0x00, 0x03, 0xFF, 0xFD, 0xFF, 0x02, 0xFE, 0xFE, 0xFE)
	return append(d, coded...)
}

func stream(pw, ph, rw, rh uint32, coded []byte) []byte {
	return append(seg(0, 48, pageInfo(pw, ph)), seg(1, 38, region(rw, rh, coded))...)
}

func allocated(f func()) uint64 {
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	d, err := os.ReadFile(filepath.Join("testdata", "jbig2", name))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestEstimateCoversAllocation(t *testing.T) {
	ctx := context.Background()
	for _, b := range []*Bitmap{noise(1000, 1000, 0, 5, 3), NewBitmap(4000, 3000), noise(33, 4000, 0, 6, 2)} {
		var data []byte
		got := allocated(func() { data, _ = Encode(ctx, b, Limits{}) })
		if est := uint64(encodeEstimate(b)); got > est {
			t.Errorf("Encode %dx%d allocated %d; estimate %d", b.W, b.H, got, est)
		}
		var est int64
		got = allocated(func() {
			_, err := ij.DecodeStream(ctx, nil, data, ij.Limits{PagePixels: DefaultPagePixels, Regions: DefaultRegions, Symbols: DefaultSymbols},
				func(e int64) (func(), error) { est = e; return func() {}, nil })
			if err != nil {
				t.Fatal(err)
			}
		})
		if got > uint64(est) {
			t.Errorf("Decode %dx%d allocated %d; estimate %d", b.W, b.H, got, est)
		}
	}
	// Symbol mode: dictionary in the globals, text region on the page.
	globals, page := fixture(t, "symbol-globals.jb2"), fixture(t, "symbol-page.jb2")
	var est int64
	got := allocated(func() {
		_, err := ij.DecodeStream(ctx, globals, page, ij.Limits{PagePixels: DefaultPagePixels, Regions: DefaultRegions, Symbols: DefaultSymbols},
			func(e int64) (func(), error) { est = e; return func() {}, nil })
		if err != nil {
			t.Fatal(err)
		}
	})
	if got > uint64(est) {
		t.Errorf("symbol Decode allocated %d; estimate %d", got, est)
	}
}

func TestRefusesBeforeAllocating(t *testing.T) {
	ctx := context.Background()
	globals := fixture(t, "symbol-globals.jb2")
	forged := bytes.Clone(globals)
	binary.BigEndian.PutUint32(forged[21:], 0xFFFFFFFF) // SDNUMEXSYMS
	binary.BigEndian.PutUint32(forged[25:], 0xFFFFFFFF) // SDNUMNEWSYMS

	for _, c := range []struct {
		name         string
		data, glob   []byte
		lim          Limits
		memory       int64
		want         error
		wantLimitErr string
	}{
		{"page width", stream(0xFFFFFFFF, 1, 1, 1, nil), nil, Limits{}, 0, ErrLimit, "PagePixels"},
		{"page pixels", stream(100000, 100000, 1, 1, nil), nil, Limits{}, 0, ErrLimit, "PagePixels"},
		{"region pixels", stream(1<<20, 1, 1<<20, 4096, nil), nil, Limits{}, 0, ErrLimit, ""},
		{"smaller limit", stream(2000, 2000, 2000, 2000, nil), nil, Limits{PagePixels: 1 << 20}, 0, ErrLimit, "PagePixels"},
		{"symbol dictionary", fixture(t, "symbol-page.jb2"), forged, Limits{}, 0, ErrLimit, "Symbols"},
		{"symbols limit", fixture(t, "symbol-page.jb2"), globals, Limits{Symbols: 4}, 0, ErrLimit, "Symbols"},
		{"regions limit", fixture(t, "symbol-page.jb2"), globals, Limits{Regions: 1}, 0, ErrLimit, "Regions"},
		{"memory", stream(8000, 8000, 8000, 8000, nil), nil, Limits{}, 1 << 20, ErrLimit, "MemoryLimit"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.memory > 0 {
				SetMemoryLimit(c.memory)
				defer SetMemoryLimit(0)
			}
			var err error
			got := allocated(func() { _, err = Decode(ctx, c.data, c.glob, c.lim) })
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v; want %v", err, c.want)
			}
			var le *LimitError
			if c.wantLimitErr != "" && (!errors.As(err, &le) || le.Name != c.wantLimitErr) {
				t.Fatalf("err = %#v; want LimitError %q", err, c.wantLimitErr)
			}
			if got >= 1<<20 {
				t.Errorf("refusal allocated %d bytes; want < 1 MiB", got)
			}
		})
	}
	// Encode refuses from the dimensions alone.
	var err error
	b := NewBitmap(1000, 1000)
	if got := allocated(func() { _, err = Encode(ctx, b, Limits{PagePixels: 1000}) }); !errors.Is(err, ErrLimit) || got >= 1<<20 {
		t.Errorf("Encode over PagePixels: err = %v, allocated %d", err, got)
	}
}

func TestBudgetHolds(t *testing.T) {
	data, err := Encode(context.Background(), noise(2000, 2000, 0, 8, 3), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var est int64
	if _, err := ij.DecodeStream(context.Background(), nil, data, ij.Limits{PagePixels: DefaultPagePixels, Regions: DefaultRegions, Symbols: DefaultSymbols},
		func(e int64) (func(), error) { est = e; return func() {}, nil }); err != nil {
		t.Fatal(err)
	}
	limit := est*5/2 + 1 // room for two decodes, never three
	SetMemoryLimit(limit)
	defer SetMemoryLimit(0)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ok, refused int
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Decode(context.Background(), data, nil, Limits{})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrMemoryLimit):
				refused++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ok == 0 {
		t.Error("no decode fit the budget")
	}
	if used, peak := memoryUsage(); used != 0 || peak > limit {
		t.Errorf("used = %d (want 0), peak = %d (limit %d)", used, peak, limit)
	}
}

func TestDefaultsAreSafe(t *testing.T) {
	l, err := Limits{}.resolve()
	if err != nil || l.PagePixels != 500_000_000 || l.Symbols != 1<<20 || l.Regions != 1<<16 {
		t.Errorf("defaults = %+v, %v", l, err)
	}
	if _, err := (Limits{Symbols: -1}).resolve(); !errors.Is(err, ErrInvalid) {
		t.Errorf("negative limit: err = %v; want ErrInvalid", err)
	}
	if defaultMemoryLimit() <= 0 {
		t.Error("default memory limit must be positive")
	}
	// A hostile header under the defaults is refused, not decoded.
	if _, err := Decode(context.Background(), stream(0xFFFFFFFF, 0xFFFFFFFF, 1, 1, nil), nil, Limits{}); !errors.Is(err, ErrLimit) {
		t.Errorf("err = %v; want ErrLimit", err)
	}
}

func TestCancellationLatency(t *testing.T) {
	// 4096 x 4096 coded pixel by pixel (no TPGDON): ~16.8 Mpx of MQ decisions.
	data := stream(4096, 4096, 4096, 4096, make([]byte, 64))
	b := NewBitmap(8192, 8192)
	for y := range b.H { // no two rows alike, so TPGDON skips nothing
		binary.BigEndian.PutUint64(b.Pix[y*b.Stride:], uint64(y)*0x9E3779B97F4A7C15)
	}
	for _, c := range []struct {
		name string
		run  func(ctx context.Context) error
	}{
		{"Decode", func(ctx context.Context) error { _, err := Decode(ctx, data, nil, Limits{}); return err }},
		{"Encode", func(ctx context.Context) error { _, err := Encode(ctx, b, Limits{}); return err }},
	} {
		ctx, cancel := context.WithCancel(context.Background())
		var at time.Time
		time.AfterFunc(20*time.Millisecond, func() { at = time.Now(); cancel() })
		err := c.run(ctx)
		done := time.Now()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%s: err = %v; want context.Canceled", c.name, err)
		}
		if d := done.Sub(at); d > 250*time.Millisecond {
			t.Errorf("%s: returned %v after cancel", c.name, d)
		}
	}
}

func TestImpl(t *testing.T) {
	if got := Impl(); got != "generic" {
		t.Errorf("Impl() = %q; want generic (no asm in this package)", got)
	}
}

// The coder is integer arithmetic. A float type or math.FMA in a reference
// would round differently once the compiler fuses it on arm64.
func TestNoFusedOpsInReferences(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	more, _ := filepath.Glob("internal/jbig2/*.go")
	for _, f := range append(files, more...) {
		if filepath.Ext(f) != ".go" || len(f) > 8 && f[len(f)-8:] == "_test.go" {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Ident:
				if n.Name == "float32" || n.Name == "float64" || n.Name == "FMA" {
					t.Errorf("%s: %s", f, n.Name)
				}
			case *ast.ImportSpec:
				if n.Path.Value == `"C"` {
					t.Errorf("%s: imports C", f)
				}
			}
			return true
		})
	}
}

func FuzzDecode(f *testing.F) {
	for _, n := range []string{"border", "column", "empty", "full", "noise", "odd", "row", "single", "symbol-page", "text"} {
		d, err := os.ReadFile(filepath.Join("testdata", "jbig2", n+".jb2"))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(d, []byte(nil))
	}
	g, _ := os.ReadFile(filepath.Join("testdata", "jbig2", "symbol-globals.jb2"))
	p, _ := os.ReadFile(filepath.Join("testdata", "jbig2", "symbol-page.jb2"))
	f.Add(p, g)
	SetMemoryLimit(64 << 20)
	f.Cleanup(func() { SetMemoryLimit(0) })
	f.Fuzz(func(t *testing.T, data, globals []byte) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = Decode(ctx, data, globals, Limits{PagePixels: 1 << 22})
	})
}
