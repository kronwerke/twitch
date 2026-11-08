package app

import (
	"sync"

	"github.com/kronwerke/twitch/internal/irc"
)

// event is what the dashboard's live stream carries: a chat line or a new overview.
type event struct {
	Kind    string       `json:"kind"` // message, state
	Message *irc.Message `json:"message,omitempty"`
	State   *Overview    `json:"state,omitempty"`
}

// hub keeps the last chat lines and hands every new event to the open dashboards.
type hub struct {
	mu   sync.Mutex
	max  int
	ring []irc.Message
	subs map[chan event]struct{}
}

func (h *hub) add(m irc.Message) {
	h.mu.Lock()
	h.ring = append(h.ring, m)
	if len(h.ring) > h.max {
		h.ring = h.ring[len(h.ring)-h.max:]
	}
	h.mu.Unlock()
	h.publish(event{Kind: "message", Message: &m})
}

func (h *hub) publish(e event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs {
		select {
		case c <- e:
		default: // a slow dashboard misses lines rather than stalling the bot
		}
	}
}

func (h *hub) recent(n int, channel string) []irc.Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []irc.Message
	for i := len(h.ring) - 1; i >= 0 && len(out) < n; i-- {
		if channel == "" || h.ring[i].Channel == channel {
			out = append(out, h.ring[i])
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (h *hub) subscribe() chan event {
	c := make(chan event, 256)
	h.mu.Lock()
	if h.subs == nil {
		h.subs = map[chan event]struct{}{}
	}
	h.subs[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (h *hub) unsubscribe(c chan event) {
	h.mu.Lock()
	delete(h.subs, c)
	h.mu.Unlock()
}
