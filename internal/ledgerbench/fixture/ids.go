package fixture

import (
	"encoding/binary"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
)

// idClock mints UUIDv7 ids from given times with seeded random bits, so a
// build is repeatable. Ids minted in non-decreasing time order also sort in
// that order: within one millisecond the 12-bit rand_a field counts up.
type idClock struct {
	rng     *rand.Rand
	lastMs  int64
	counter uint16
}

func newIDClock(rng *rand.Rand) *idClock {
	return &idClock{rng: rng, lastMs: -1}
}

// at returns a new id whose timestamp is at's millisecond.
func (c *idClock) at(at time.Time) uuid.UUID {
	ms := at.UnixMilli()
	if ms == c.lastMs && c.counter < 0x0fff {
		c.counter++
	} else {
		c.lastMs = ms
		c.counter = 0
	}
	var id uuid.UUID
	id[0] = byte(ms >> 40)
	id[1] = byte(ms >> 32)
	id[2] = byte(ms >> 24)
	id[3] = byte(ms >> 16)
	id[4] = byte(ms >> 8)
	id[5] = byte(ms)
	id[6] = 0x70 | byte(c.counter>>8)
	id[7] = byte(c.counter)
	binary.BigEndian.PutUint64(id[8:], c.rng.Uint64())
	id[8] = 0x80 | (id[8] & 0x3f)
	return id
}
