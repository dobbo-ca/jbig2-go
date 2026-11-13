package jbig2

import (
	"context"
	"errors"
	"testing"
)

func encoded(t *testing.T, w, h int) []byte {
	t.Helper()
	data, err := Encode(context.Background(), noise(w, h, (w+7)/8, 1, 3), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func isLimit(err error, name string) bool {
	var le *LimitError
	return errors.As(err, &le) && le.Name == name
}

func TestReservedMaxBytes(t *testing.T) {
	data := encoded(t, 256, 256)
	SetMemoryLimit(1) // the pool would refuse anything; Reserved must not touch it
	defer SetMemoryLimit(0)
	u0, p0 := memoryUsage()

	// A declared 8000x8000 page needs 8 MB; refusing must allocate far less.
	big := stream(8000, 8000, 8000, 8000, nil)
	a := allocated(func() {
		_, err := Decode(context.Background(), big, nil, Limits{MaxBytes: 1000, Reserved: true})
		if !isLimit(err, "MaxBytes") {
			t.Errorf("err = %v; want MaxBytes LimitError", err)
		}
	})
	if a > 1<<18 {
		t.Errorf("refused call allocated %d bytes", a)
	}
	if _, err := Decode(context.Background(), data, nil, Limits{MaxBytes: 1 << 30, Reserved: true}); err != nil {
		t.Fatal(err)
	}
	if u, p := memoryUsage(); u != u0 || p != p0 {
		t.Errorf("pool moved: used %d->%d peak %d->%d", u0, u, p0, p)
	}
	bm := NewBitmap(8000, 8000)
	a = allocated(func() {
		if _, err := Encode(context.Background(), bm, Limits{MaxBytes: 10, Reserved: true}); !isLimit(err, "MaxBytes") {
			t.Errorf("Encode err = %v; want MaxBytes LimitError", err)
		}
	})
	if a > 1<<18 {
		t.Errorf("refused Encode allocated %d bytes", a)
	}
	if _, err := Encode(context.Background(), NewBitmap(64, 64), Limits{MaxBytes: 1 << 30, Reserved: true}); err != nil {
		t.Errorf("Encode: %v", err)
	}
}

func TestReservedInvalid(t *testing.T) {
	data := encoded(t, 16, 16)
	for _, l := range []Limits{{Reserved: true}, {MaxBytes: -1}} {
		if _, err := Decode(context.Background(), data, nil, l); !errors.Is(err, ErrInvalid) {
			t.Errorf("Decode %+v: %v; want ErrInvalid", l, err)
		}
		if _, err := Encode(context.Background(), NewBitmap(8, 8), l); !errors.Is(err, ErrInvalid) {
			t.Errorf("Encode %+v: %v; want ErrInvalid", l, err)
		}
	}
}

func TestMaxBytesWithoutReservedUsesPool(t *testing.T) {
	data := encoded(t, 256, 256)
	defer SetMemoryLimit(0)
	if _, err := Decode(context.Background(), data, nil, Limits{MaxBytes: 1000}); !isLimit(err, "MaxBytes") {
		t.Errorf("err = %v; want MaxBytes LimitError", err)
	}
	SetMemoryLimit(0)
	_, p0 := memoryUsage()
	if _, err := Decode(context.Background(), data, nil, Limits{MaxBytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	if _, p := memoryUsage(); p <= p0 {
		t.Errorf("pool peak %d did not rise above %d", p, p0)
	}
	SetMemoryLimit(1)
	if _, err := Decode(context.Background(), data, nil, Limits{MaxBytes: 1 << 30}); !isLimit(err, "MemoryLimit") {
		t.Errorf("err = %v; want pool refusal", err)
	}
}

func TestPoolNeverFitsIsPermanent(t *testing.T) {
	data := encoded(t, 256, 256)
	defer SetMemoryLimit(0)
	SetMemoryLimit(1)
	_, err := Decode(context.Background(), data, nil, Limits{})
	if !isLimit(err, "MemoryLimit") || errors.Is(err, ErrMemoryLimit) {
		t.Errorf("err = %v; want MemoryLimit LimitError, not ErrMemoryLimit", err)
	}
}

func TestPoolBusyIsTransient(t *testing.T) {
	data := encoded(t, 256, 256)
	defer SetMemoryLimit(0)
	const lim = 4 << 20
	SetMemoryLimit(lim)
	release, err := reserve(lim - 10)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := Decode(context.Background(), data, nil, Limits{}); !errors.Is(err, ErrMemoryLimit) {
		t.Errorf("err = %v; want ErrMemoryLimit", err)
	}
}

func TestPageSize(t *testing.T) {
	data := encoded(t, 300, 200)
	if w, h, err := PageSize(data, nil); err != nil || w != 300 || h != 200 {
		t.Errorf("PageSize = %d,%d,%v; want 300,200", w, h, err)
	}
	// Page information (11-byte header + 19 bytes) as globals, the region as the stream.
	if w, h, err := PageSize(data[30:], data[:30]); err != nil || w != 300 || h != 200 {
		t.Errorf("with globals = %d,%d,%v; want 300,200", w, h, err)
	}
	pg, err := Decode(context.Background(), data[30:], data[:30], Limits{})
	if err != nil || pg.W != 300 || pg.H != 200 {
		t.Fatalf("Decode with globals: %v", err)
	}
	if w, h, err := PageSize(data[:30], nil); err != nil || w != 300 || h != 200 {
		t.Errorf("truncated after page info = %d,%d,%v", w, h, err)
	}
}

func TestPageSizeErrors(t *testing.T) {
	for name, d := range map[string][]byte{
		"unknown height": seg(0, 48, pageInfo(100, 0xFFFFFFFF)),
		"no page info":   seg(1, 38, region(8, 8, make([]byte, 4))),
		"garbage":        []byte("not a jbig2 stream"),
		"empty":          nil,
	} {
		if _, _, err := PageSize(d, nil); !errors.Is(err, ErrNoPageSize) {
			t.Errorf("%s: err = %v; want ErrNoPageSize", name, err)
		}
	}
}

func TestPageSizeAllocatesNothingPageSized(t *testing.T) {
	d := seg(0, 48, pageInfo(8000, 8000)) // 8 MB packed if it were allocated
	a := allocated(func() {
		if w, h, err := PageSize(d, nil); err != nil || w != 8000 || h != 8000 {
			t.Errorf("PageSize = %d,%d,%v", w, h, err)
		}
	})
	if a > 64<<10 {
		t.Errorf("PageSize allocated %d bytes", a)
	}
}

func TestDecodeCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Decode(ctx, encoded(t, 64, 64), nil, Limits{}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want context.Canceled", err)
	}
}
