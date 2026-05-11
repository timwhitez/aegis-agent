package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"go-cli-agent/internal/events"
)

type Renderer struct {
	json bool
	out  io.Writer
}

func New(jsonMode bool, out io.Writer) *Renderer {
	if out == nil {
		out = os.Stdout
	}
	return &Renderer{json: jsonMode, out: out}
}

func (r *Renderer) Handle(evt events.Event) {
	if r.json {
		_ = json.NewEncoder(r.out).Encode(evt)
		return
	}
	switch evt.Type {
	case "session.started":
		fmt.Fprintf(r.out, "== session:start ==\nsession: %s\nsteer: go-cli-agent steer %s --message \"...\"\n", evt.SessionID, evt.SessionID)
	case "provider.call":
		fmt.Fprintf(r.out, "== provider:%v ==\n", evt.Data["provider"])
	case "assistant.message":
		if text, ok := evt.Data["text"].(string); ok && text != "" {
			fmt.Fprintln(r.out, "== assistant ==")
			fmt.Fprintln(r.out, text)
		}
	case "tool.after":
		fmt.Fprintf(r.out, "== tool:%v ==\n", evt.Data["tool_name"])
		if text, ok := evt.Data["display_output"].(string); ok && text != "" {
			fmt.Fprintln(r.out, text)
		}
	case "session.awaiting_input":
		fmt.Fprintf(r.out, "== awaiting_input ==\nsession: %s\nnext: go-cli-agent continue %s --message \"...\"\n", evt.SessionID, evt.SessionID)
	case "session.completed":
		fmt.Fprintf(r.out, "== completed ==\nsession: %s\n", evt.SessionID)
	case "session.child.queued":
		fmt.Fprintf(r.out, "== child:queued ==\njob: %v\n", evt.Data["job_id"])
	case "queue.job.claimed":
		fmt.Fprintf(r.out, "== queue:claimed ==\njob: %v\n", evt.Data["job_id"])
	case "queue.job.completed":
		fmt.Fprintf(r.out, "== queue:completed ==\njob: %v\n", evt.Data["job_id"])
	case "queue.job.failed":
		fmt.Fprintf(r.out, "== queue:failed ==\njob: %v\n", evt.Data["job_id"])
	case "session.background.accepted":
		fmt.Fprintf(r.out, "== background:accepted ==\ncount: %v\n", evt.Data["count"])
	case "session.paused":
		fmt.Fprintf(r.out, "== paused ==\nsession: %s\nnext: go-cli-agent continue %s --message \"...\"\n", evt.SessionID, evt.SessionID)
	case "session.failed":
		fmt.Fprintf(r.out, "== failed ==\nsession: %s\nnext: go-cli-agent continue %s --message \"...\"\n", evt.SessionID, evt.SessionID)
	case "session.steer.accepted":
		fmt.Fprintf(r.out, "== steer:accepted ==\n")
	case "goal.created":
		fmt.Fprintf(r.out, "== goal:created ==\nstatus: %v\n", evt.Data["status"])
	case "goal.updated":
		fmt.Fprintf(r.out, "== goal:updated ==\nstatus: %v\n", evt.Data["status"])
	case "goal.budget_limited":
		fmt.Fprintf(r.out, "== goal:budget_limited ==\nsession: %s\n", evt.SessionID)
	case "goal.completed":
		fmt.Fprintf(r.out, "== goal:completed ==\nsession: %s\n", evt.SessionID)
	}
}
