package websocket

import (
	"errors"
	"runtime"
	"testing"
)

func TestInflater_BoundsMemoryByTheLimit(t *testing.T) {
	const limit = 1 << 20
	const inflatedSize = 64 << 20
	bomb := deflateBomb(t, inflatedSize)

	t.Run("should stop a bomb at the limit without allocating its inflated size", func(t *testing.T) {
		inflater := newInflater(false)

		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		out, err := inflater.inflate(bomb, limit)
		runtime.ReadMemStats(&after)

		if !errors.Is(err, errInflatedTooLarge) || out != nil {
			t.Fatalf("\nwanted:\nnil, %v\ngot:\n%d bytes, %v", errInflatedTooLarge, len(out), err)
		}
		// Inflating to the limit allocates a few times the limit as the
		// output buffer grows. The bomb's inflated size is 64 times the
		// limit, so a bound of 8 times the limit shows inflating stopped.
		const bound = 8 * limit
		if allocated := after.TotalAlloc - before.TotalAlloc; allocated > bound {
			t.Fatalf("\nwanted allocated bytes:\nat most %d\ngot:\n%d", bound, allocated)
		}
	})
}
