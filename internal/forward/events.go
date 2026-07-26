package forward

import "time"

// EventType enumerates forward-related events emitted by Forward.
type EventType string

const (
	EventReady   EventType = "ready"
	EventDropped EventType = "dropped"
	EventStopped EventType = "stopped"
	EventLog     EventType = "log"
	EventStale   EventType = "stale"
	EventError   EventType = "error"
)

// Event is a single forward-related notification.
type Event struct {
	Type      EventType `json:"type"`
	ForwardID string    `json:"forward_id"`
	Status    Status    `json:"status,omitempty"`
	Message   string    `json:"message,omitempty"`
	Stream    string    `json:"stream,omitempty"` // "out" or "err" for log events
	Line      string    `json:"line,omitempty"`    // for log events
	Time      time.Time `json:"time"`
}
