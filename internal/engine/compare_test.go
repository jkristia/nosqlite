package engine

// compare_test.go covers the cross-type total order directly.
//
// These tests live next to the code rather than with the public query tests
// because they poke at unexported things (`compare`, `absent`) that only a test
// inside this package can see. Everything reachable through nosqlite.Find is
// still tested from the parent package.

import "testing"

func TestCompareTotalOrder(t *testing.T) {
	// absent < null < numbers < strings < booleans < arrays < objects
	ordered := []any{
		absent,
		nil,
		float64(-1),
		float64(0),
		float64(10),
		"a",
		"b",
		false,
		true,
		[]any{float64(1)},
		[]any{float64(1), float64(2)},
		map[string]any{"a": float64(1)},
	}
	for i := 0; i < len(ordered); i++ {
		for j := 0; j < len(ordered); j++ {
			got := compare(ordered[i], ordered[j])
			var want int
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}
			if got != want {
				t.Errorf("compare(%v, %v) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
}

func TestCompareMixedNumericTypes(t *testing.T) {
	// A filter written in Go may hold an int; a document always holds float64.
	if compare(float64(30), 30) != 0 {
		t.Error("float64(30) should compare equal to int(30)")
	}
	if compare(float64(29), 30) != -1 {
		t.Error("29 should sort before 30")
	}
}
