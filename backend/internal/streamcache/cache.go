package streamcache

import (
	"encoding/json"
	"sync"
	"time"
)

type Event struct {
	Sequence int64
	Name     string
	Data     json.RawMessage
}

type Snapshot struct {
	Events   []Event
	Terminal bool
	Gap      bool
	Wait     <-chan struct{}
	Found    bool
}

type Cache struct {
	mu        sync.Mutex
	ttl       time.Duration
	maxEvents int
	streams   map[string]*entry
}

type entry struct {
	userID    string
	sequence  int64
	events    []Event
	terminal  bool
	expiresAt time.Time
	notify    chan struct{}
}

func New(ttl time.Duration, maxEvents int) *Cache {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if maxEvents <= 0 {
		maxEvents = 10_000
	}
	return &Cache{ttl: ttl, maxEvents: maxEvents, streams: make(map[string]*entry)}
}

func (c *Cache) Start(messageID, userID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(time.Now())
	c.streams[messageID] = &entry{
		userID: userID, expiresAt: time.Now().Add(c.ttl), notify: make(chan struct{}),
	}
}

func (c *Cache) Append(messageID, name string, payload map[string]any, terminal bool) (Event, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	item, ok := c.streams[messageID]
	if !ok {
		return Event{}, false
	}
	item.sequence++
	data := make(map[string]any, len(payload)+2)
	for key, value := range payload {
		data[key] = value
	}
	data["messageId"] = messageID
	data["sequence"] = item.sequence
	raw, _ := json.Marshal(data)
	event := Event{Sequence: item.sequence, Name: name, Data: raw}
	item.events = append(item.events, event)
	if len(item.events) > c.maxEvents {
		item.events = item.events[len(item.events)-c.maxEvents:]
	}
	item.terminal = item.terminal || terminal
	item.expiresAt = time.Now().Add(c.ttl)
	close(item.notify)
	item.notify = make(chan struct{})
	return event, true
}

func (c *Cache) Snapshot(messageID, userID string, after int64) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(time.Now())
	item, ok := c.streams[messageID]
	if !ok || item.userID != userID {
		return Snapshot{}
	}
	result := Snapshot{Found: true, Terminal: item.terminal, Wait: item.notify}
	if len(item.events) > 0 && after < item.events[0].Sequence-1 {
		result.Gap = true
		return result
	}
	for _, event := range item.events {
		if event.Sequence > after {
			result.Events = append(result.Events, event)
		}
	}
	return result
}

func (c *Cache) cleanupLocked(now time.Time) {
	for id, item := range c.streams {
		if now.After(item.expiresAt) {
			close(item.notify)
			delete(c.streams, id)
		}
	}
}
