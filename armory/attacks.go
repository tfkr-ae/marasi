package armory

import (
	"errors"
	"fmt"

	"github.com/tfkr-ae/marasi/wordlist"
)

// produceHarpoon generates requests by targeting each template position in turn.
func (manager *Manager) produceHarpoon(execution *execution, tmpl *armoryTemplate, wordlistName string) error {
	for position := 0; position < tmpl.positionCount; position++ {
		iterator, err := manager.wordlists.Open(wordlistName)
		if err != nil {
			return fmt.Errorf("opening wordlist %s: %w", wordlistName, err)
		}

		payloads := make(map[int]string, 1)

		for iterator.Scan() {
			payloads[position] = iterator.Text()

			if err := manager.queue(execution, tmpl, payloads); err != nil {
				iterator.Close()
				return err
			}
		}

		if err := iterator.Err(); err != nil {
			iterator.Close()
			return fmt.Errorf("reading wordlist %s: %w", wordlistName, err)
		}

		if err := iterator.Close(); err != nil {
			return fmt.Errorf("closing wordlist %s: %w", wordlistName, err)
		}
	}

	return nil
}

// produceBroadside generates requests by applying each payload to every template position.
func (manager *Manager) produceBroadside(execution *execution, tmpl *armoryTemplate, wordlistName string) error {
	iterator, err := manager.wordlists.Open(wordlistName)
	if err != nil {
		return fmt.Errorf("opening wordlist %s: %w", wordlistName, err)
	}

	payloads := make(map[int]string, tmpl.positionCount)

	for iterator.Scan() {
		payload := iterator.Text()
		for position := 0; position < tmpl.positionCount; position++ {
			payloads[position] = payload
		}

		if err := manager.queue(execution, tmpl, payloads); err != nil {
			iterator.Close()
			return err
		}
	}

	if err := iterator.Err(); err != nil {
		iterator.Close()
		return fmt.Errorf("reading wordlist %s: %w", wordlistName, err)
	}

	if err := iterator.Close(); err != nil {
		return fmt.Errorf("closing wordlist %s: %w", wordlistName, err)
	}

	return nil
}

// produceTandem generates requests by advancing one wordlist per template position together.
func (manager *Manager) produceTandem(execution *execution, tmpl *armoryTemplate, wordlistNames []string) error {
	if len(wordlistNames) == 0 {
		return errors.New("tandem requires at least one wordlist")
	}

	iterators := make([]wordlist.Iterator, 0, len(wordlistNames))
	for _, name := range wordlistNames {
		iterator, err := manager.wordlists.Open(name)
		if err != nil {
			closeWordlistIterators(iterators, wordlistNames[:len(iterators)])
			return fmt.Errorf("opening wordlist %s: %w", name, err)
		}
		iterators = append(iterators, iterator)
	}

	payloads := make(map[int]string, len(iterators))
	for {
		complete := true
		for _, iterator := range iterators {
			if !iterator.Scan() {
				complete = false
			}
		}

		if !complete {
			for index, iterator := range iterators {
				if err := iterator.Err(); err != nil {
					closeWordlistIterators(iterators, wordlistNames)
					return fmt.Errorf("reading wordlist %s: %w", wordlistNames[index], err)
				}
			}
			return closeWordlistIterators(iterators, wordlistNames)
		}

		for position, iterator := range iterators {
			payloads[position] = iterator.Text()
		}

		if err := manager.queue(execution, tmpl, payloads); err != nil {
			closeWordlistIterators(iterators, wordlistNames)
			return err
		}
	}
}

// produceMaelstrom generates every combination across the position wordlists.
func (manager *Manager) produceMaelstrom(execution *execution, tmpl *armoryTemplate, wordlistNames []string) error {
	if len(wordlistNames) == 0 {
		return errors.New("maelstrom requires at least one wordlist")
	}

	payloads := make(map[int]string, len(wordlistNames))
	var generate func(position int) error
	generate = func(position int) error {
		select {
		case <-execution.ctx.Done():
			return execution.ctx.Err()
		default:
		}

		wordlistName := wordlistNames[position]
		iterator, err := manager.wordlists.Open(wordlistName)
		if err != nil {
			return fmt.Errorf("opening wordlist %s: %w", wordlistName, err)
		}

		for iterator.Scan() {
			payloads[position] = iterator.Text()

			if position < len(wordlistNames)-1 {
				if err := generate(position + 1); err != nil {
					iterator.Close()
					return err
				}
				continue
			}

			if err := manager.queue(execution, tmpl, payloads); err != nil {
				iterator.Close()
				return err
			}
		}

		if err := iterator.Err(); err != nil {
			iterator.Close()
			return fmt.Errorf("reading wordlist %s: %w", wordlistName, err)
		}

		if err := iterator.Close(); err != nil {
			return fmt.Errorf("closing wordlist %s: %w", wordlistName, err)
		}

		return nil
	}

	return generate(0)
}

// queue renders the request for payloads and queues it for the workers. A
// request whose messages do not encode is recorded as a failed attempt
// instead, and queue returns nil so the run goes on. Any other render
// error, a failed record or a stopped run ends the producer.
func (manager *Manager) queue(execution *execution, tmpl *armoryTemplate, payloads map[int]string) error {
	raw, err := tmpl.render(payloads)
	var unsent *messageError
	if errors.As(err, &unsent) {
		return manager.recordFailedAttempt(execution, payloads, unsent)
	}
	if err != nil {
		return err
	}
	select {
	case execution.requests <- raw:
		return nil
	case <-execution.ctx.Done():
		return execution.ctx.Err()
	}
}

// closeWordlistIterators closes each iterator and returns the first close error.
func closeWordlistIterators(iterators []wordlist.Iterator, wordlistNames []string) error {
	var firstErr error
	for index, iterator := range iterators {
		if err := iterator.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("closing wordlist %s: %w", wordlistNames[index], err)
		}
	}
	return firstErr
}
