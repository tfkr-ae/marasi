package service

import "testing"

func TestAPILevel(t *testing.T) {
	t.Run("should be level 1", func(t *testing.T) {
		// A bump is a breaking change for clients: update the bump rule's
		// docs and this value together, never one alone.
		if APILevel != 1 {
			t.Fatalf("\nwanted:\n1\ngot:\n%d", APILevel)
		}
	})
}
