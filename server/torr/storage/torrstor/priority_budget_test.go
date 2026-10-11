package torrstor

import "testing"

// priorityBudget divides the connection limit between viewers. The cases that
// matter are the ones where naive division breaks: more viewers than the
// limit, or a limit of zero.
func TestPriorityBudget(t *testing.T) {
	cases := []struct {
		name   string
		limit  int
		member int
		want   int
	}{
		{"one viewer gets the whole limit", 60, 1, 60},
		{"two viewers split it", 60, 2, 30},
		{"three viewers split it", 60, 3, 20},
		{"the live default", 25, 1, 25},
		// More viewers than the limit allows. Plain division gives zero or a
		// negative, and the assignment loop would never run: every viewer
		// stalls with peers connected. The floor keeps one piece moving.
		{"more viewers than the limit still get one piece", 25, 26, 1},
		{"far more viewers than the limit still get one piece", 25, 100, 1},
		{"a limit of one shared by many still gets one piece", 1, 10, 1},
		{"no readers means the limit is untouched", 60, 0, 60},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := priorityBudget(c.limit, c.member); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}

// The budget must never exceed the limit it divides. A caller that multiplied
// instead of dividing would hand every viewer the full limit and defeat the
// sharing the limit exists for.
func TestPriorityBudgetNeverExceedsLimit(t *testing.T) {
	for _, limit := range []int{1, 25, 60} {
		for _, readers := range []int{1, 2, 5, 26, 100} {
			if got := priorityBudget(limit, readers); got > limit {
				t.Fatalf("limit %d, %d readers: got %d, above the limit", limit, readers, got)
			}
		}
	}
}
