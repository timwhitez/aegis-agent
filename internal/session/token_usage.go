package session

import "math"

// TokenSum rejects negative counters and saturates positive overflow.
func TokenSum(values ...int64) (int64, bool) {
	var total int64
	for _, value := range values {
		if value < 0 {
			return 0, false
		}
	}
	for _, value := range values {
		if value > math.MaxInt64-total {
			return math.MaxInt64, true
		}
		total += value
	}
	return total, true
}
