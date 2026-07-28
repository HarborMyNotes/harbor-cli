// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-07-28

package client

// ListTasks returns the user's tasks as a collection envelope. Accepts limit,
// offset, order, status (active|today|done|all), and due_before (epoch-ms)
// params; empty values are dropped by doGet so server defaults apply.
func (c *Client) ListTasks(params map[string]string) ([]byte, error) {
	return c.doGet("/tasks", params)
}

// ListNoteTasks returns the tasks owned by one note (its <harbor-task> blocks)
// as a collection envelope, ordered by in-note position. This is a READ of the
// note's task list — it cannot change which note a task belongs to. Accepts
// limit, offset, and order params.
func (c *Client) ListNoteTasks(noteID string, params map[string]string) ([]byte, error) {
	return c.doGet("/notes/"+noteID+"/tasks", params)
}

// GetTask fetches a single task by id, returning the {task, usn} envelope.
func (c *Client) GetTask(id string) ([]byte, error) {
	return c.doGet("/tasks/"+id, nil)
}

// CreateTask creates a task, returning the {task, usn} envelope with 201.
//
// The body deliberately never carries note_id: linking a task to a note also
// has to write the note's <harbor-task> block, and a link without the block is
// deleted by the note's next save. See cmd/tasks.go for the full story and
// HarborMyNotes/app.harbor.my#1094 for the endpoint that does it safely.
func (c *Client) CreateTask(body map[string]any) ([]byte, error) {
	return c.doPost("/tasks", body)
}

// UpdateTask applies a partial update to a task, returning {task, usn}. Only
// the fields present in body are changed; the clear_due_at /
// clear_reminder_at / clear_recurrence booleans null a nullable column.
func (c *Client) UpdateTask(id string, body map[string]any) ([]byte, error) {
	return c.doPatch("/tasks/"+id, body)
}

// CompleteTask marks a task done, returning {task, usn}. When body is non-nil
// it may carry a done_time (epoch-ms) override; otherwise the server stamps
// the current time. Completing a RECURRING task advances it to its next
// occurrence instead of closing it, so the returned task has done_at unset and
// a rolled-forward due_at.
func (c *Client) CompleteTask(id string, body map[string]any) ([]byte, error) {
	if body == nil {
		body = map[string]any{}
	}
	return c.doPost("/tasks/"+id+"/done", body)
}

// UncompleteTask reopens a completed task (nulls done_at), returning
// {task, usn}. It is idempotent and does not rewind recurrence.
func (c *Client) UncompleteTask(id string) ([]byte, error) {
	return c.doDelete("/tasks/"+id+"/done", nil)
}

// DeleteTask soft-deletes (tombstones) a task so the deletion syncs to every
// device, returning 204 with an empty body. Deleting a note-linked task also
// strips its block from the owning note.
func (c *Client) DeleteTask(id string) ([]byte, error) {
	return c.doDelete("/tasks/"+id, nil)
}
