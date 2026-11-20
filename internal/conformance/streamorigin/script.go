package streamorigin

import (
	"fmt"
	"time"

	"github.com/tfkr-ae/marasi/internal/conformance/h2"
)

// sender is one side's end of a case's one request/response pair.
type sender interface {
	head(fields []h2.Field, end bool) error
	message(data []byte, end bool) error
	trailers(fields []h2.Field) error
}

// runScript runs one side's steps. awaited counts, per kind, the arrivals
// the side has already waited for. A timed-out Await is recorded and
// counted as awaited, so the next Await waits for the arrival after it.
// An Await for KindEnd waits endTimeout.
func runScript(t *transcript, side string, steps []Step, s sender, awaited map[string]int, timeout, endTimeout time.Duration) error {
	s = &codingSender{sender: s}
	for i, step := range steps {
		var err error
		switch step.Kind {
		case StepHead:
			err = s.head(step.Fields, step.End)
		case StepMessage:
			err = s.message([]byte(step.Data), step.End)
		case StepTrailers:
			err = s.trailers(step.Fields)
		case StepAnswer:
			t.add(Arrival{Side: SideOrigin, Kind: KindAnswer})
		case StepAwait:
			awaited[step.Await]++
			limit := timeout
			if step.Await == KindEnd {
				limit = endTimeout
			}
			if !t.await(side, step.Await, awaited[step.Await], limit) {
				t.add(Arrival{Side: side, Kind: KindTimeout, Awaited: step.Await})
			}
		default:
			err = fmt.Errorf("unknown step kind %q", step.Kind)
		}
		if err != nil {
			return fmt.Errorf("%s step %d (%s): %w", side, i, step.Kind, err)
		}
	}
	return nil
}
