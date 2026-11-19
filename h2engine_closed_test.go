package marasi

import "testing"

func TestH2ClosedStreams(t *testing.T) {
	t.Run("should tell closed streams from skipped ones among recent IDs while keeping a bounded record", func(t *testing.T) {
		var closed h2ClosedStreams
		for id := uint32(1); id <= 199_999; id += 2 {
			closed.add(id)
		}
		closed.add(200_003)

		for _, id := range []uint32{1, 199_999, 200_003} {
			if !closed.has(id) {
				t.Errorf("stream %d\nwanted:\nclosed\ngot:\nnot closed", id)
			}
		}
		if closed.has(200_001) {
			t.Errorf("stream 200001, which the client skipped\nwanted:\nnot closed\ngot:\nclosed")
		}
		if kept := len(closed.ids); kept > 2*h2ClosedKept {
			t.Errorf("the stream IDs kept\nwanted:\nat most %d\ngot:\n%d", 2*h2ClosedKept, kept)
		}
	})
}
