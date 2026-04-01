package quota

import "fmt"

func Resolve(requested, defaultQuota int) (int, error) {
	if requested < 0 {
		return 0, fmt.Errorf("quota must be positive")
	}
	if requested == 0 {
		return 0, nil
	}
	return requested, nil
}
