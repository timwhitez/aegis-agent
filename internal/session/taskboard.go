package session

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

type TaskCreateInput struct {
	Subject     string   `json:"subject"`
	Description string   `json:"description,omitempty"`
	Priority    string   `json:"priority,omitempty"`
	BlockedBy   []string `json:"blocked_by,omitempty"`
	Labels      []string `json:"labels,omitempty"`
}

type TaskUpdateInput struct {
	TaskID          string   `json:"task_id"`
	Status          string   `json:"status,omitempty"`
	Subject         string   `json:"subject,omitempty"`
	Description     string   `json:"description,omitempty"`
	Priority        string   `json:"priority,omitempty"`
	Owner           string   `json:"owner,omitempty"`
	AddBlockedBy    []string `json:"add_blocked_by,omitempty"`
	RemoveBlockedBy []string `json:"remove_blocked_by,omitempty"`
	AddBlocks       []string `json:"add_blocks,omitempty"`
	RemoveBlocks    []string `json:"remove_blocks,omitempty"`
	AppendNote      string   `json:"append_note,omitempty"`
}

func CreateTask(store *Store, sessionID string, input TaskCreateInput) (Task, error) {
	if input.Subject == "" {
		return Task{}, errors.New("subject is required")
	}
	taskID, err := store.NextTaskID(sessionID)
	if err != nil {
		return Task{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	task := Task{
		ID:          taskID,
		Subject:     input.Subject,
		Description: input.Description,
		Status:      "pending",
		Priority:    defaultPriority(input.Priority),
		BlockedBy:   uniqueStrings(input.BlockedBy),
		Blocks:      []string{},
		Labels:      uniqueStrings(input.Labels),
		Notes:       []string{},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	tasks, err := store.ListTasks(sessionID)
	if err != nil {
		return Task{}, err
	}
	tasks = append(tasks, task)
	syncTaskEdges(tasks, task.ID, nil, nil, task.BlockedBy, task.Blocks)
	if err := ensureTaskReferences(tasks); err != nil {
		return Task{}, err
	}
	if err := ensureAcyclic(tasks); err != nil {
		return Task{}, err
	}
	if err := store.SaveTasks(sessionID, tasks); err != nil {
		return Task{}, err
	}
	return findTask(tasks, taskID)
}

func UpdateTask(store *Store, sessionID string, input TaskUpdateInput) (Task, error) {
	tasks, err := store.ListTasks(sessionID)
	if err != nil {
		return Task{}, err
	}
	index := -1
	for i, task := range tasks {
		if task.ID == input.TaskID {
			index = i
			break
		}
	}
	if index < 0 {
		return Task{}, fmt.Errorf("task not found: %s", input.TaskID)
	}
	task := tasks[index]
	previousBlockedBy := append([]string{}, task.BlockedBy...)
	previousBlocks := append([]string{}, task.Blocks...)
	prevStatus := task.Status
	if input.Status != "" {
		switch input.Status {
		case "pending", "in_progress", "completed", "cancelled":
			task.Status = input.Status
		default:
			return Task{}, fmt.Errorf("invalid status: %s", input.Status)
		}
	}
	if input.Subject != "" {
		task.Subject = input.Subject
	}
	if input.Description != "" {
		task.Description = input.Description
	}
	if input.Priority != "" {
		task.Priority = defaultPriority(input.Priority)
	}
	if input.Owner != "" {
		task.Owner = input.Owner
	}
	task.BlockedBy = uniqueStrings(removeStrings(append(task.BlockedBy, input.AddBlockedBy...), input.RemoveBlockedBy...))
	task.Blocks = uniqueStrings(removeStrings(append(task.Blocks, input.AddBlocks...), input.RemoveBlocks...))
	if input.AppendNote != "" {
		task.Notes = append(task.Notes, input.AppendNote)
	}
	task.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	tasks[index] = task
	syncTaskEdges(tasks, task.ID, previousBlockedBy, previousBlocks, task.BlockedBy, task.Blocks)
	if err := ensureTaskReferences(tasks); err != nil {
		return Task{}, err
	}
	if err := ensureAcyclic(tasks); err != nil {
		return Task{}, err
	}
	if prevStatus != "completed" && task.Status == "completed" {
		unlockDependents(tasks, task.ID)
	}
	if err := store.SaveTasks(sessionID, tasks); err != nil {
		return Task{}, err
	}
	return findTask(tasks, task.ID)
}

func BuildTaskBoard(todo []TodoItem, tasks []Task) TaskBoard {
	if todo == nil {
		todo = []TodoItem{}
	}
	if tasks == nil {
		tasks = []Task{}
	}
	ready := []Task{}
	blocked := []Task{}
	inProgress := []Task{}
	done := []Task{}
	for _, task := range tasks {
		switch {
		case task.Status == "in_progress":
			inProgress = append(inProgress, task)
		case task.Status == "completed" || task.Status == "cancelled":
			done = append(done, task)
		case task.Status == "pending" && len(task.BlockedBy) == 0:
			ready = append(ready, task)
		case task.Status == "pending" && len(task.BlockedBy) > 0:
			blocked = append(blocked, task)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].ID < ready[j].ID })
	sort.Slice(blocked, func(i, j int) bool { return blocked[i].ID < blocked[j].ID })
	sort.Slice(inProgress, func(i, j int) bool { return inProgress[i].ID < inProgress[j].ID })
	sort.Slice(done, func(i, j int) bool { return done[i].ID < done[j].ID })
	return TaskBoard{
		Todo:  todo,
		Tasks: tasks,
		Counters: map[string]int{
			"todo":        len(todo),
			"tasks":       len(tasks),
			"in_progress": len(inProgress),
			"ready":       len(ready),
			"blocked":     len(blocked),
			"completed":   len(done),
		},
		Groups: map[string][]Task{
			"in_progress": inProgress,
			"ready":       ready,
			"blocked":     blocked,
			"completed":   done,
		},
	}
}

func findTask(tasks []Task, taskID string) (Task, error) {
	for _, task := range tasks {
		if task.ID == taskID {
			return task, nil
		}
	}
	return Task{}, fmt.Errorf("task not found: %s", taskID)
}

func syncTaskEdges(tasks []Task, taskID string, previousBlockedBy, previousBlocks, currentBlockedBy, currentBlocks []string) {
	index := map[string]int{}
	for i := range tasks {
		tasks[i].BlockedBy = uniqueStrings(tasks[i].BlockedBy)
		tasks[i].Blocks = uniqueStrings(tasks[i].Blocks)
		index[tasks[i].ID] = i
	}

	for _, dependency := range removeStrings(previousBlockedBy, currentBlockedBy...) {
		if target, ok := index[dependency]; ok {
			tasks[target].Blocks = removeStrings(tasks[target].Blocks, taskID)
		}
	}
	for _, dependency := range currentBlockedBy {
		if target, ok := index[dependency]; ok {
			tasks[target].Blocks = uniqueStrings(append(tasks[target].Blocks, taskID))
		}
	}
	for _, dependent := range removeStrings(previousBlocks, currentBlocks...) {
		if target, ok := index[dependent]; ok {
			tasks[target].BlockedBy = removeStrings(tasks[target].BlockedBy, taskID)
		}
	}
	for _, dependent := range currentBlocks {
		if target, ok := index[dependent]; ok {
			tasks[target].BlockedBy = uniqueStrings(append(tasks[target].BlockedBy, taskID))
		}
	}
}

func ensureTaskReferences(tasks []Task) error {
	index := map[string]struct{}{}
	for _, task := range tasks {
		index[task.ID] = struct{}{}
	}
	for _, task := range tasks {
		for _, dependency := range append([]string{}, append(task.BlockedBy, task.Blocks...)...) {
			if _, ok := index[dependency]; !ok {
				return fmt.Errorf("unknown task reference: %s", dependency)
			}
		}
	}
	return nil
}

func ensureAcyclic(tasks []Task) error {
	adj := map[string][]string{}
	for _, task := range tasks {
		adj[task.ID] = append([]string{}, task.Blocks...)
	}
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var dfs func(string) bool
	dfs = func(node string) bool {
		if visiting[node] {
			return true
		}
		if visited[node] {
			return false
		}
		visiting[node] = true
		for _, next := range adj[node] {
			if dfs(next) {
				return true
			}
		}
		visiting[node] = false
		visited[node] = true
		return false
	}
	for _, task := range tasks {
		if dfs(task.ID) {
			return errors.New("task graph contains a cycle")
		}
	}
	return nil
}

func unlockDependents(tasks []Task, completedID string) {
	var unlocked []string
	for i := range tasks {
		if tasks[i].ID == completedID {
			continue
		}
		next := removeStrings(tasks[i].BlockedBy, completedID)
		if len(next) != len(tasks[i].BlockedBy) {
			tasks[i].BlockedBy = next
			tasks[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			unlocked = append(unlocked, tasks[i].ID)
		}
	}
	for i := range tasks {
		if tasks[i].ID != completedID {
			continue
		}
		next := removeStrings(tasks[i].Blocks, unlocked...)
		if len(next) != len(tasks[i].Blocks) {
			tasks[i].Blocks = next
			tasks[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
	}
}

func removeStrings(input []string, remove ...string) []string {
	removeSet := map[string]struct{}{}
	for _, item := range remove {
		removeSet[item] = struct{}{}
	}
	var out []string
	for _, item := range input {
		if _, ok := removeSet[item]; ok {
			continue
		}
		out = append(out, item)
	}
	return out
}

func uniqueStrings(input []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, item := range input {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func defaultPriority(priority string) string {
	switch priority {
	case "high", "medium", "low":
		return priority
	case "":
		return "medium"
	default:
		return "medium"
	}
}
