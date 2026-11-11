package app

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/kronwerke/twitch/internal/store"
	"github.com/kronwerke/twitch/internal/twitch"
)

// Overview is everything the dashboard shows, in one answer.
type Overview struct {
	Version    string            `json:"version"`
	Started    time.Time         `json:"started"`
	Configured bool              `json:"configured"` // a Twitch application is set
	Callback   string            `json:"callback"`   // the redirect URL the application needs
	Bot        *BotView          `json:"bot"`
	Chat       ChatView          `json:"chat"`
	Channels   []ChannelView     `json:"channels"`
	Commands   []store.Command   `json:"commands"`
	Timers     []store.Timer     `json:"timers"`
	Settings   map[string]string `json:"settings"`
	Users      []store.User      `json:"users"`
	LiveAt     time.Time         `json:"live_at"`
	LiveErr    string            `json:"live_error,omitempty"`
	Start      string            `json:"start"`     // the season start in words
	Countdown  string            `json:"countdown"` // until then
	Update     string            `json:"update,omitempty"`
}

// BotView is the bot account without its tokens.
type BotView struct {
	Login   string    `json:"login"`
	Name    string    `json:"name"`
	Linked  time.Time `json:"linked"`
	By      string    `json:"by"`
	Scopes  []string  `json:"scopes"`
	Expires time.Time `json:"expires"`
}

// ChatView is the state of the chat connection.
type ChatView struct {
	Connected bool     `json:"connected"`
	Detail    string   `json:"detail"`
	Joined    []string `json:"joined"`
}

// ChannelView is a channel with its live state.
type ChannelView struct {
	store.Channel
	Image  string         `json:"image,omitempty"`
	Live   *twitch.Stream `json:"live,omitempty"`
	Joined bool           `json:"joined"`
}

func (a *App) overview() *Overview {
	o := &Overview{Version: a.cfg.Version, Started: a.started, Configured: a.tw.Configured(), Callback: a.callbackURL(),
		Channels: []ChannelView{}, Commands: []store.Command{}, Timers: []store.Timer{}, Users: []store.User{}}
	joined := map[string]bool{}
	for _, l := range a.chat.Joined() {
		joined[l] = true
	}
	connected, _ := a.chat.Connected()
	o.Chat = ChatView{Connected: connected, Joined: append([]string{}, a.chat.Joined()...)}
	if s := a.ircState.Load(); s != nil {
		o.Chat.Detail = *s
	}
	a.store.View(func(d *store.Data) {
		if d.Bot != nil {
			o.Bot = &BotView{Login: d.Bot.Login, Name: d.Bot.Name, Linked: d.Bot.Linked, By: d.Bot.By, Scopes: d.Bot.Scopes, Expires: d.Bot.Expires}
		}
		o.Commands = append([]store.Command{}, d.Commands...)
		o.Timers = append([]store.Timer{}, d.Timers...)
		o.Users = append([]store.User{}, d.Users...)
		o.Settings = map[string]string{}
		for k, v := range d.Settings {
			o.Settings[k] = v
		}
		a.liveMu.RLock()
		for _, c := range d.Channels {
			v := ChannelView{Channel: c, Joined: joined[c.Login]}
			if u, ok := a.users[c.Login]; ok {
				v.Image = u.Image
				if v.Name == "" {
					v.Name = u.DisplayName
				}
			}
			if s, ok := a.live[c.Login]; ok {
				s := s
				v.Live = &s
			}
			o.Channels = append(o.Channels, v)
		}
		o.LiveAt, o.LiveErr = a.liveAt, a.liveErr
		a.liveMu.RUnlock()
	})
	sort.Slice(o.Commands, func(i, j int) bool { return o.Commands[i].Name < o.Commands[j].Name })
	start := a.start()
	o.Start, o.Countdown = a.date(start), a.countdown(start)
	return o
}

// LiveEntry is one live streamer for the website.
type LiveEntry struct {
	Login     string    `json:"login"`
	Name      string    `json:"name"`
	Title     string    `json:"title"`
	Game      string    `json:"game"`
	Viewers   int       `json:"viewers"`
	StartedAt time.Time `json:"started_at"`
	Thumbnail string    `json:"thumbnail"`
	Image     string    `json:"image,omitempty"`
	URL       string    `json:"url"`
}

// liveNow lists the channels marked for the website that are live, most viewers first.
func (a *App) liveNow() []LiveEntry {
	show := map[string]bool{}
	a.store.View(func(d *store.Data) {
		for _, c := range d.Channels {
			if c.Website {
				show[c.Login] = true
			}
		}
	})
	a.liveMu.RLock()
	defer a.liveMu.RUnlock()
	out := []LiveEntry{}
	for l, s := range a.live {
		if !show[l] {
			continue
		}
		e := LiveEntry{Login: l, Name: s.UserName, Title: s.Title, Game: s.Game, Viewers: s.Viewers, StartedAt: s.StartedAt,
			Thumbnail: thumb(s.Thumbnail), URL: "https://www.twitch.tv/" + l}
		if u, ok := a.users[l]; ok {
			e.Image = u.Image
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Viewers > out[j].Viewers })
	return out
}

func thumb(t string) string {
	return strings.NewReplacer("{width}", "640", "{height}", "360").Replace(t)
}

func (a *App) callbackURL() string { return a.cfg.PublicURL + "/auth/callback" }

// ---- self update ----

func (a *App) updateLoop(ctx context.Context) {
	select {
	case <-time.After(30 * time.Second):
	case <-ctx.Done():
		return
	}
	for {
		a.checkUpdate(ctx)
		select {
		case <-time.After(10 * time.Minute):
		case <-ctx.Done():
			return
		}
	}
}

func (a *App) checkUpdate(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			a.events.add("panic in update: %v", r)
		}
	}()
	u := a.cfg.Updater
	r, newer, err := u.Check(ctx)
	if err != nil {
		a.events.add("update check: %v", err)
		return
	}
	if !newer {
		return
	}
	a.events.add("installing %s (running %s)", r.Tag, a.cfg.Version)
	if err := u.Install(ctx, r); err != nil {
		a.events.add("update to %s: %v", r.Tag, err)
		return
	}
	select {
	case a.restart <- "update to " + r.Tag:
	default:
	}
}
