// SPDX-License-Identifier: AGPL-3.0-or-later

package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// UPID is a parsed Proxmox task identifier. Most PVE writes that matter
// (create/clone/start/stop/migrate/destroy a guest, backups) run as background
// tasks and return a UPID string of the form:
//
//	UPID:NODE:PID:PSTART:STARTTIME:TYPE:ID:USER:
type UPID struct {
	Raw  string
	Node string
	Type string
}

// ParseUPID parses a UPID string. It returns an error if s is not a UPID.
func ParseUPID(s string) (UPID, error) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) < 8 || parts[0] != "UPID" || parts[1] == "" {
		return UPID{}, fmt.Errorf("not a UPID: %q", s)
	}
	return UPID{Raw: s, Node: parts[1], Type: parts[5]}, nil
}

// UPIDFromData reports whether a Proxmox response's `data` value is a UPID string
// (as returned by Post/Put for async operations) and parses it.
func UPIDFromData(data []byte) (UPID, bool) {
	var s string
	if json.Unmarshal(data, &s) != nil {
		return UPID{}, false
	}
	u, err := ParseUPID(s)
	if err != nil {
		return UPID{}, false
	}
	return u, true
}

// TaskStatus is the result of GET /nodes/{node}/tasks/{upid}/status.
type TaskStatus struct {
	Status     string `json:"status"`     // "running" | "stopped"
	ExitStatus string `json:"exitstatus"` // "OK" or an error string once stopped
}

// Done reports whether the task has finished (success or failure).
func (t TaskStatus) Done() bool { return t.Status == "stopped" }

// OK reports whether the task finished successfully.
func (t TaskStatus) OK() bool { return t.ExitStatus == "OK" }

// TaskWait polls a task's status until it reaches a terminal state or ctx is
// done. poll defaults to 2s. It returns the terminal status; callers check OK().
func (c *Client) TaskWait(ctx context.Context, u UPID, poll time.Duration) (TaskStatus, error) {
	if poll <= 0 {
		poll = 2 * time.Second
	}
	path := fmt.Sprintf("/nodes/%s/tasks/%s/status", u.Node, url.PathEscape(u.Raw))
	for {
		data, err := c.Get(path)
		if err != nil {
			return TaskStatus{}, err
		}
		var st TaskStatus
		if err := json.Unmarshal(data, &st); err != nil {
			return TaskStatus{}, fmt.Errorf("proxmox task status: %w", err)
		}
		if st.Done() {
			return st, nil
		}
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// TaskLogTail fetches the last lines of a task's log, for enriching a failure
// diagnostic. Best-effort: returns "" on any error.
func (c *Client) TaskLogTail(u UPID, limit int) string {
	if limit <= 0 {
		limit = 25
	}
	data, err := c.Get(fmt.Sprintf("/nodes/%s/tasks/%s/log?limit=%d", u.Node, url.PathEscape(u.Raw), limit))
	if err != nil {
		return ""
	}
	// data is an array of {n, t} line records.
	var lines []struct {
		T string `json:"t"`
	}
	if json.Unmarshal(data, &lines) != nil {
		return ""
	}
	var b strings.Builder
	for _, ln := range lines {
		b.WriteString(ln.T)
		b.WriteByte('\n')
	}
	return b.String()
}
