package runtime

import "aegis-agent/internal/events"

// SetApprovalBudgetBarriersForTest exposes only test interleaving points to the
// external integration test, which can import the real Web service without a
// production runtime -> Web dependency.
func SetApprovalBudgetBarriersForTest(r *Runner, beforeBudget, contextLoaded func()) {
	r.engine.beforeGoalBudgetWrapUpTurn = beforeBudget
	r.engine.beforeAppendEvent = func(evt events.Event) {
		if evt.Type == "session.context.loaded" {
			contextLoaded()
		}
	}
}
