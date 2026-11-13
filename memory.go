package jbig2

import (
	"fmt"
	"math"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
)

var mem struct {
	sync.Mutex
	limit, used, peak int64 // limit 0: derive the default
}

// SetMemoryLimit sets the bytes all concurrent calls may hold between them.
// A call reserves its estimated peak before allocating and returns
// ErrMemoryLimit if that does not fit now, or a *LimitError named
// "MemoryLimit" if it could never fit, even with nothing else in use. n <= 0 restores the default: half of
// GOMEMLIMIT or of the cgroup limit, whichever is lower, or 1 GiB with neither.
func SetMemoryLimit(n int64) {
	mem.Lock()
	defer mem.Unlock()
	mem.limit, mem.peak = max(n, 0), mem.used
}

func reserve(n int64) (func(), error) {
	mem.Lock()
	defer mem.Unlock()
	limit := mem.limit
	if limit == 0 {
		limit = defaultMemoryLimit()
	}
	if n > limit {
		return nil, &LimitError{Name: "MemoryLimit", Got: n, Max: limit}
	}
	if n > limit-mem.used {
		return nil, fmt.Errorf("%w: need %d bytes, %d of %d in use", ErrMemoryLimit, n, mem.used, limit)
	}
	mem.used += n
	mem.peak = max(mem.peak, mem.used)
	return func() {
		mem.Lock()
		mem.used -= n
		mem.Unlock()
	}, nil
}

func memoryUsage() (used, peak int64) {
	mem.Lock()
	defer mem.Unlock()
	return mem.used, mem.peak
}

func defaultMemoryLimit() int64 {
	lim := int64(math.MaxInt64)
	if g := debug.SetMemoryLimit(-1); g < lim {
		lim = g
	}
	for _, f := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		if b, err := os.ReadFile(f); err == nil {
			if v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil && v > 0 {
				lim = min(lim, v)
			}
		}
	}
	if lim == math.MaxInt64 {
		return 1 << 30
	}
	return lim / 2
}
