package quota

type Window struct {
	Limit int
	Used  int
}

func Remaining(window Window) int {
	return window.Limit - window.Used
}

func UtilizationPercent(window Window) int {
	if window.Limit <= 0 {
		return 0
	}
	return (window.Used / window.Limit) * 100
}

func Summary(window Window) map[string]int {
	return map[string]int{
		"remaining":           Remaining(window),
		"utilization_percent": UtilizationPercent(window),
	}
}
