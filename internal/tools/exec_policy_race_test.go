//go:build race

package tools

// execPolicyRaceEnabled reports whether the binary was built with the race
// detector. Cost assertions that measure wall clock have to relax under
// instrumentation; allocation bounds stay enforced.
const execPolicyRaceEnabled = true
