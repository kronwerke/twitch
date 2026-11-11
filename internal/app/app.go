// Package app is the Kronwerke Twitch bot: it sits in the chats of the streamers the
// team picked, answers commands, posts timers, shows every chat in one place and tells
// the website who is live. Everything is changed in the dashboard and takes effect at
// once, without a restart.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kronwerke/twitch/internal/irc"
	"github.com/kronwerke/twitch/internal/store"
	"github.com/kronwerke/twitch/internal/twitch"
	"github.com/kronwerke/twitch/internal/update"
)

// Config comes from the environment.
type Config struct {
	ClientID, ClientSecret string
	PublicURL              string
	Owner                  string
	AdminToken             string
	HTTPAddr               string
	StatePath              string
	Version                string
	IRCAddr                string
	Updater                *update.Updater
}

// BotScopes are what the bot account grants: reading and writing chat.
var BotScopes = []string{"chat:read", "chat:edit", "user:read:chat", "user:write:chat", "user:bot"}

// App holds everything.
type App struct {
	cfg Config
	// the season start the server announces (time.Time), see start()
	serverStart atomic.Value
	log         *slog.Logger
	store       *store.Store
	tw          *twitch.Client
	chat        *irc.Client
	started     time.Time

	hub hub

	liveMu  sync.RWMutex
	live    map[string]twitch.Stream
	liveAt  time.Time
	liveErr string
	users   map[string]twitch.User // profile pictures and names of the channels

	cdMu      sync.Mutex
	cooldowns map[string]time.Time // channel/command -> next allowed use

	timerMu sync.Mutex
	timers  map[string]*timerState // channel/timer id

	ircState atomic.Pointer[string]
	events   eventLog
	restart  chan string
	ssePing  time.Duration // how often the live stream says it is alive, 25 s when zero
}

type timerState struct {
	last  time.Time
	lines int
}

// New opens the state and seeds the first commands and timers.
func New(cfg Config, log *slog.Logger) (*App, error) {
	st, err := store.Open(cfg.StatePath)
	if err != nil {
		return nil, err
	}
	a := &App{
		cfg: cfg, log: log, store: st, started: time.Now(),
		tw:        twitch.New(cfg.ClientID, cfg.ClientSecret),
		live:      map[string]twitch.Stream{},
		users:     map[string]twitch.User{},
		cooldowns: map[string]time.Time{},
		timers:    map[string]*timerState{},
		restart:   make(chan string, 1),
	}
	a.hub.max = 600
	a.events.max = 300
	a.events.log = log
	a.seed()
	a.chat = &irc.Client{
		Addr:      cfg.IRCAddr,
		Creds:     a.botCredentials,
		OnMessage: a.onMessage,
		OnState: func(connected bool, detail string) {
			a.ircState.Store(&detail)
			if connected && cfg.Updater != nil {
				cfg.Updater.Settle() // the new binary reached Twitch: the trial is over
			}
			a.events.add("chat: %s", detail)
			a.hub.publish(event{Kind: "state", State: a.overview()})
		},
	}
	return a, nil
}

// ErrRestart asks main to exit so systemd starts the new binary.
var ErrRestart = errors.New("restart requested")

// Run serves until ctx ends or an update wants a restart.
func (a *App) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go a.serveHTTP(ctx)
	if a.cfg.Updater != nil {
		go a.updateLoop(ctx)
	}
	a.chat.SetChannels(a.store.EnabledChannels())
	go a.chat.Run(ctx)
	go a.loop(ctx, time.Minute, a.refreshLive)
	go a.loop(ctx, 10*time.Minute, a.refreshStart)
	go a.refreshStart(ctx)
	go a.loop(ctx, 30*time.Second, a.tickTimers)
	go a.loop(ctx, time.Hour, func(context.Context) { a.store.SweepSessions(time.Now()) })
	go a.refreshLive(ctx)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case why := <-a.restart:
		a.log.Info("restarting", "why", why)
		return ErrRestart
	}
}

func (a *App) loop(ctx context.Context, every time.Duration, fn func(context.Context)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						a.events.add("panic: %v", r)
					}
				}()
				fn(ctx)
			}()
		}
	}
}

// ---- the bot account ----

// botCredentials gives the chat client a token that is valid for at least ten minutes.
func (a *App) botCredentials(ctx context.Context) (string, string, error) {
	var bot *store.Bot
	a.store.View(func(d *store.Data) {
		if d.Bot != nil {
			b := *d.Bot
			bot = &b
		}
	})
	if bot == nil {
		return "", "", errors.New("noch kein Bot-Konto verbunden")
	}
	if time.Until(bot.Expires) > 10*time.Minute {
		return bot.Login, bot.Access, nil
	}
	t, err := a.tw.Renew(ctx, bot.Refresh)
	if err != nil {
		var he *twitch.HTTPError
		if errors.As(err, &he) && (he.Status == 400 || he.Status == 401) {
			return "", "", errors.New("Twitch hat das Bot-Konto abgemeldet, bitte im Dashboard neu verbinden")
		}
		return "", "", err
	}
	err = a.store.Update(func(d *store.Data) error {
		if d.Bot == nil {
			return errors.New("bot account removed")
		}
		d.Bot.Access, d.Bot.Expires = t.Access, time.Now().Add(time.Duration(t.ExpiresIn)*time.Second)
		if t.Refresh != "" {
			d.Bot.Refresh = t.Refresh
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	a.events.add("bot token renewed")
	return bot.Login, t.Access, nil
}

// ---- chat ----

func (a *App) onMessage(m irc.Message) {
	a.hub.add(m)
	if m.Kind != "chat" || m.Self {
		return
	}
	a.countLine(m.Channel)
	if strings.HasPrefix(m.Text, "!") {
		a.command(m)
	}
}

// Say posts as the bot and shows the message in the feed.
func (a *App) Say(channel, text string) error {
	m, err := a.chat.Say(channel, text)
	if err != nil {
		return err
	}
	a.hub.add(m)
	return nil
}

func (a *App) command(m irc.Message) {
	name, args, _ := strings.Cut(strings.TrimPrefix(m.Text, "!"), " ")
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return
	}
	var cmd *store.Command
	a.store.View(func(d *store.Data) {
		for i := range d.Commands {
			c := d.Commands[i]
			if c.Name == name || contains(c.Aliases, name) {
				cmd = &c
				return
			}
		}
	})
	if cmd == nil || !cmd.Enabled || (cmd.ModOnly && !m.Privileged()) {
		return
	}
	if len(cmd.Channels) > 0 && !contains(cmd.Channels, m.Channel) {
		return
	}
	key := m.Channel + "/" + cmd.Name
	a.cdMu.Lock()
	if until, ok := a.cooldowns[key]; ok && time.Now().Before(until) && !m.Privileged() {
		a.cdMu.Unlock()
		return
	}
	a.cooldowns[key] = time.Now().Add(time.Duration(cmd.Cooldown) * time.Second)
	a.cdMu.Unlock()
	text := a.fill(cmd.Response, m.Channel, m.User, strings.TrimSpace(args))
	if err := a.Say(m.Channel, text); err != nil {
		a.events.add("!%s in %s: %v", cmd.Name, m.Channel, err)
		return
	}
	a.store.Update(func(d *store.Data) error {
		for i := range d.Commands {
			if d.Commands[i].Name == cmd.Name {
				d.Commands[i].Uses++
			}
		}
		return nil
	})
}

// fill puts the placeholders into a text.
func (a *App) fill(text, channel, user, args string) string {
	start := a.start()
	r := strings.NewReplacer(
		"{user}", user,
		"{channel}", a.displayName(channel),
		"{args}", cleanArgs(args),
		"{start}", a.date(start),
		"{countdown}", a.countdown(start),
		"{discord}", a.store.Setting("discord", "https://discord.gg/DZs3XyyHDj"),
		"{website}", a.store.Setting("website", "https://kronwerke.com"),
		"{server}", a.store.Setting("server", "kronwerke.net"),
		"{live}", a.liveList(channel),
	)
	return r.Replace(text)
}

// argsLinks matches what would turn {args} into a link: a scheme or a host with a dot.
var argsLinks = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://\S*|\b[\w-]+(\.[\w-]+)*\.(com|net|org|de|gg|tv|io|ly|me|co|xyz|ru|info|app|link|to|be)\b\S*)`)

// cleanArgs is what a viewer typed after a command, made safe to repeat as the bot (which
// is a moderator, so AutoMod does not check it): no links, no chat commands, short.
func cleanArgs(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Join(strings.Fields(argsLinks.ReplaceAllString(s, "")), " ")
	s = strings.TrimLeft(s, "/.!")
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60])
	}
	return strings.TrimSpace(s)
}

func berlin() *time.Location {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		loc = time.FixedZone("CET", 3600)
	}
	return loc
}

// start is the season start: the setting "start" when the team set one here, else what
// the server announces (Kronwerke Core's seasonStart through the bot's status API), else
// the planned 16 January 2027, 18:00.
func (a *App) start() time.Time {
	loc := berlin()
	if t, err := time.ParseInLocation("2006-01-02T15:04", a.store.Setting("start", ""), loc); err == nil {
		return t
	}
	if t, ok := a.serverStart.Load().(time.Time); ok && !t.IsZero() {
		return t
	}
	return time.Date(2027, 1, 16, 18, 0, 0, 0, loc)
}

// refreshStart reads the start the server announces.
func (a *App) refreshStart(ctx context.Context) {
	url := a.store.Setting("season.status", "https://kronwerke.com/api/status")
	if url == "" || url == "-" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return
	}
	defer res.Body.Close()
	var body struct {
		Season struct {
			Start string `json:"start"`
		} `json:"season"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body) != nil {
		return
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04", body.Season.Start, berlin()); err == nil {
		a.serverStart.Store(t)
	}
}

// date and countdown speak the chat's language (setting "lang": de or en).
func (a *App) date(t time.Time) string {
	if a.store.Setting("lang", "de") == "en" {
		return englishDate(t)
	}
	return germanDate(t)
}

func (a *App) countdown(start time.Time) string {
	if a.store.Setting("lang", "de") == "en" {
		return countdownEN(time.Now(), start)
	}
	return countdown(time.Now(), start)
}

func englishDate(t time.Time) string {
	h := t.Hour() % 12
	if h == 0 {
		h = 12
	}
	ampm := "am"
	if t.Hour() >= 12 {
		ampm = "pm"
	}
	clock := fmt.Sprintf("%d %s", h, ampm)
	if t.Minute() != 0 {
		clock = fmt.Sprintf("%d:%02d %s", h, t.Minute(), ampm)
	}
	return fmt.Sprintf("%s, %s %d, %d, at %s German time", t.Weekday(), t.Month(), t.Day(), t.Year(), clock)
}

func countdownEN(now, start time.Time) string {
	d := start.Sub(now)
	if d <= 0 {
		return "Season 2 is running"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	plural := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	switch {
	case days > 0:
		return plural(days, "day", "days") + " and " + plural(hours, "hour", "hours") + " to go"
	case hours > 0:
		return plural(hours, "hour", "hours") + " and " + plural(mins, "minute", "minutes") + " to go"
	default:
		return plural(max(mins, 1), "minute", "minutes") + " to go"
	}
}

var weekdays = []string{"Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"}
var months = []string{"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"}

func germanDate(t time.Time) string {
	s := fmt.Sprintf("%s, %d. %s %d, um %d Uhr", weekdays[t.Weekday()], t.Day(), months[t.Month()-1], t.Year(), t.Hour())
	if t.Minute() != 0 {
		s = fmt.Sprintf("%s, %d. %s %d, um %d:%02d Uhr", weekdays[t.Weekday()], t.Day(), months[t.Month()-1], t.Year(), t.Hour(), t.Minute())
	}
	return s
}

// countdown says how long until the start, in German.
func countdown(now, start time.Time) string {
	d := start.Sub(now)
	if d <= 0 {
		return "Season 2 läuft"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	plural := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	switch {
	case days > 0:
		return "noch " + plural(days, "Tag", "Tage") + " und " + plural(hours, "Stunde", "Stunden")
	case hours > 0:
		return "noch " + plural(hours, "Stunde", "Stunden") + " und " + plural(mins, "Minute", "Minuten")
	default:
		return "noch " + plural(max(mins, 1), "Minute", "Minuten")
	}
}

func (a *App) displayName(login string) string {
	a.liveMu.RLock()
	defer a.liveMu.RUnlock()
	if u, ok := a.users[login]; ok && u.DisplayName != "" {
		return u.DisplayName
	}
	return login
}

// liveList names the other Kronwerke streamers who are live, for {live}.
func (a *App) liveList(except string) string {
	a.liveMu.RLock()
	defer a.liveMu.RUnlock()
	var names []string
	for l, s := range a.live {
		if l != except {
			names = append(names, s.UserName)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "gerade niemand sonst"
	}
	return strings.Join(names, ", ")
}

// ---- timers ----

func (a *App) countLine(channel string) {
	a.timerMu.Lock()
	defer a.timerMu.Unlock()
	for k, s := range a.timers {
		if strings.HasPrefix(k, channel+"/") {
			s.lines++
		}
	}
}

func (a *App) tickTimers(ctx context.Context) {
	var timers []store.Timer
	a.store.View(func(d *store.Data) { timers = append(timers, d.Timers...) })
	joined := a.chat.Joined()
	now := time.Now()
	for _, t := range timers {
		if !t.Enabled || t.Minutes <= 0 || strings.TrimSpace(t.Text) == "" {
			continue
		}
		for _, ch := range joined {
			if len(t.Channels) > 0 && !contains(t.Channels, ch) {
				continue
			}
			if t.LiveOnly && !a.isLive(ch) {
				continue
			}
			key := ch + "/" + t.ID
			a.timerMu.Lock()
			s := a.timers[key]
			if s == nil {
				// first sight: start the clock at a random point, so timers of many
				// channels do not all fire in the same minute
				s = &timerState{last: now.Add(-time.Duration(rand.IntN(t.Minutes*60)) * time.Second)}
				a.timers[key] = s
			}
			due := now.Sub(s.last) >= time.Duration(t.Minutes)*time.Minute && s.lines >= t.MinLines
			if due {
				s.last, s.lines = now, 0
			}
			a.timerMu.Unlock()
			if due {
				if err := a.Say(ch, a.fill(t.Text, ch, "", "")); err != nil {
					a.events.add("timer %s in %s: %v", t.Name, ch, err)
				}
			}
		}
	}
}

// ---- who is live ----

func (a *App) isLive(login string) bool {
	a.liveMu.RLock()
	defer a.liveMu.RUnlock()
	_, ok := a.live[login]
	return ok
}

func (a *App) refreshLive(ctx context.Context) {
	if !a.tw.Configured() {
		return
	}
	var logins []string
	a.store.View(func(d *store.Data) {
		for _, c := range d.Channels {
			logins = append(logins, c.Login)
		}
	})
	if len(logins) == 0 {
		a.liveMu.Lock()
		a.live, a.liveAt, a.liveErr = map[string]twitch.Stream{}, time.Now(), ""
		a.liveMu.Unlock()
		return
	}
	streams, err := a.tw.Streams(ctx, logins)
	a.liveMu.Lock()
	if err != nil {
		a.liveErr = err.Error()
		a.liveMu.Unlock()
		a.events.add("live: %v", err)
		return
	}
	before := a.live
	a.live = map[string]twitch.Stream{}
	for _, s := range streams {
		a.live[strings.ToLower(s.UserLogin)] = s
	}
	a.liveAt, a.liveErr = time.Now(), ""
	var missing []string
	for _, l := range logins {
		if _, ok := a.users[l]; !ok {
			missing = append(missing, l)
		}
	}
	changed := len(before) != len(a.live)
	for l := range a.live {
		if _, ok := before[l]; !ok {
			changed = true
		}
	}
	a.liveMu.Unlock()
	if len(missing) > 0 {
		if users, err := a.tw.Users(ctx, missing); err == nil {
			a.liveMu.Lock()
			for _, u := range users {
				a.users[strings.ToLower(u.Login)] = u
			}
			a.liveMu.Unlock()
		}
	}
	if changed {
		a.hub.publish(event{Kind: "state", State: a.overview()})
	}
}

// ---- seeds ----

func (a *App) seed() {
	a.store.Update(func(d *store.Data) error {
		// the start was a fixed seed before 0.3.0: it follows the server now unless the team set another one
		if d.Settings["start"] == "2027-01-16T18:00" && d.Settings["start.followed"] == "" {
			delete(d.Settings, "start")
			d.Settings["start.followed"] = "1"
		}
		if d.Seeded {
			return nil
		}
		d.Seeded = true
		if owner := store.Lower(a.cfg.Owner); owner != "" && len(d.Users) == 0 {
			d.Users = append(d.Users, store.User{Login: owner, Owner: true, Added: time.Now()})
		}
		d.Settings["discord"] = "https://discord.gg/DZs3XyyHDj"
		d.Settings["website"] = "https://kronwerke.com"
		d.Settings["server"] = "kronwerke.net"
		d.Commands = append(d.Commands,
			store.Command{Name: "kronwerke", Aliases: []string{"kw"}, Enabled: true, Cooldown: 30,
				Response: "Kronwerke ist ein Modpack-Server für Streamer und ihre Communities: fünf Stufen, die der ganze Server gemeinsam freischaltet. Season 2 startet am {start}. Alles dazu: {website}"},
			store.Command{Name: "discord", Enabled: true, Cooldown: 30,
				Response: "Der Kronwerke-Discord: {discord} Dort holst du dir auch deinen Platz auf der Whitelist."},
			store.Command{Name: "start", Aliases: []string{"wann"}, Enabled: true, Cooldown: 30,
				Response: "Season 2 startet am {start}, {countdown}."},
			store.Command{Name: "whitelist", Aliases: []string{"mitspielen"}, Enabled: true, Cooldown: 30,
				Response: "So bist du dabei: in den Discord kommen ({discord}), verifizieren, dann gibt dir {channel} einen Platz auf der Whitelist."},
			store.Command{Name: "multi", Enabled: true, Cooldown: 60,
				Response: "Gerade auch live auf Kronwerke: {live}"},
		)
		d.Timers = append(d.Timers, store.Timer{ID: "t1", Name: "Start und Discord", Enabled: false, LiveOnly: true, Minutes: 30, MinLines: 15,
			Text: "Kronwerke Season 2 startet am {start}, {countdown}. Mitspielen: {discord}"})
		return nil
	})
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

// ---- recent events ----

type eventLog struct {
	mu    sync.Mutex
	max   int
	items []string
	log   *slog.Logger
}

func (e *eventLog) add(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if e.log != nil {
		e.log.Info("event", "what", msg)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.items = append(e.items, time.Now().UTC().Format("01-02 15:04:05")+" "+msg)
	if len(e.items) > e.max {
		e.items = e.items[len(e.items)-e.max:]
	}
}

func (e *eventLog) last(n int) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	n = min(n, len(e.items))
	return append([]string(nil), e.items[len(e.items)-n:]...)
}
