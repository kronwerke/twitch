// Package irc is a Twitch chat client over IRC with TLS: it reads every channel it sits
// in and writes as the bot account. It reconnects by itself and keeps under Twitch's
// limits for joins and messages.
package irc

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// Message is one line of chat, or a notice from Twitch.
type Message struct {
	ID          string    `json:"id,omitempty"`
	Channel     string    `json:"channel"`
	Login       string    `json:"login"`
	User        string    `json:"user"`
	Text        string    `json:"text"`
	Color       string    `json:"color,omitempty"`
	Mod         bool      `json:"mod,omitempty"`
	Broadcaster bool      `json:"broadcaster,omitempty"`
	VIP         bool      `json:"vip,omitempty"`
	Sub         bool      `json:"sub,omitempty"`
	Self        bool      `json:"self,omitempty"`
	Kind        string    `json:"kind"` // chat, action, notice, event, dropped
	Time        time.Time `json:"time"`
	Wait        int       `json:"wait,omitempty"`   // seconds a message of the bot waits for the slow mode
	Ref         string    `json:"ref,omitempty"`    // dropped: the id of the bot's message Twitch did not post
	Reason      string    `json:"reason,omitempty"` // dropped: why, in German
}

// Room is a channel's chat settings as Twitch reports them, and what the bot is there.
type Room struct {
	Slow          int  `json:"slow"`           // seconds between two messages of one account, 0 off
	FollowersOnly int  `json:"followers_only"` // minutes an account must follow, -1 off
	SubsOnly      bool `json:"subs_only"`
	EmoteOnly     bool `json:"emote_only"`
	Unique        bool `json:"unique"` // r9k: no message twice
	Mod           bool `json:"mod"`    // the bot is a moderator (or VIP or the broadcaster) there
	VIP           bool `json:"vip"`
	LinksBlocked  bool `json:"links_blocked"` // a message of the bot with a link was refused
	NotFollowing  bool `json:"not_following"` // followers only refused the bot: it does not follow (long enough)
	Known         bool `json:"known"`         // a ROOMSTATE came
}

// Exempt reports whether chat limits for normal accounts do not apply to the bot here.
func (r Room) Exempt() bool { return r.Mod || r.VIP }

// Muted says why the bot cannot write here at all, or "".
func (r Room) Muted() string {
	if r.Exempt() {
		return ""
	}
	switch {
	case r.SubsOnly:
		return "Nur Subs dürfen schreiben"
	case r.EmoteOnly:
		return "Nur Emotes erlaubt"
	case r.FollowersOnly >= 0 && r.NotFollowing:
		return "Nur Follower, und das Bot-Konto folgt dem Kanal nicht (lange genug)"
	}
	return ""
}

type sentLine struct {
	id   string
	text string
	at   time.Time
}

// Privileged reports whether the sender may use mod only commands.
func (m Message) Privileged() bool { return m.Mod || m.Broadcaster }

// Credentials gives the login and a valid access token of the bot account.
type Credentials func(ctx context.Context) (login, token string, err error)

// Client is one connection to Twitch chat.
type Client struct {
	Addr      string // irc.chat.twitch.tv:6697
	Creds     Credentials
	OnMessage func(Message)
	OnState   func(connected bool, detail string)
	Dial      func(ctx context.Context, addr string) (net.Conn, error) // tests

	mu        sync.Mutex
	want      map[string]bool
	joined    map[string]bool
	conn      net.Conn
	login     string
	connected bool
	sent      []time.Time
	kick      chan struct{}
	rooms     map[string]*Room
	last      map[string]sentLine // per channel, the bot's last message
	nextID    int
	// OnDropped hears of a message of the bot that Twitch refused, with Twitch's msg-id.
	OnDropped func(sent Message, msgID, reason string)
	lastMsg   map[string]Message // per channel, the bot's last message as returned by Say
}

// SetChannels says which channels to sit in; joins and parts happen at once.
func (c *Client) SetChannels(logins []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	want := map[string]bool{}
	for _, l := range logins {
		want[strings.ToLower(l)] = true
	}
	c.want = want
	if c.conn == nil {
		return
	}
	var join, part []string
	for l := range want {
		if !c.joined[l] {
			join = append(join, "#"+l)
			c.joined[l] = true
		}
	}
	for l := range c.joined {
		if !want[l] {
			part = append(part, "#"+l)
			delete(c.joined, l)
		}
	}
	sort.Strings(join)
	sort.Strings(part)
	for _, b := range batch(join, 15) {
		c.writeLocked("JOIN " + strings.Join(b, ","))
	}
	for _, b := range batch(part, 15) {
		c.writeLocked("PART " + strings.Join(b, ","))
	}
}

// Joined lists the channels the bot sits in now.
func (c *Client) Joined() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.joined))
	for l := range c.joined {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// Connected reports the state of the connection and the bot's login.
func (c *Client) Connected() (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected, c.login
}

// ErrOffline is returned when there is no connection to write to.
var ErrOffline = errors.New("nicht mit dem Twitch-Chat verbunden")

// ErrRate is returned when sending now would cross Twitch's message limit.
var ErrRate = errors.New("zu viele Nachrichten in kurzer Zeit, kurz warten")

// Say writes a message into a channel as the bot. Twitch allows 20 messages in 30
// seconds for an account that is not a moderator there; the client stays under that.
// Without moderator rights it also keeps the slow mode (the message then waits and
// goes out later, Wait says how long), makes a text unique that it sent there in the
// last 30 seconds, and does not write into a subs or emote only chat.
func (c *Client) Say(channel, text string) (Message, error) {
	channel = strings.ToLower(strings.TrimPrefix(channel, "#"))
	text = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(text, "\r", " "), "\n", " "))
	if text == "" {
		return Message{}, errors.New("leere Nachricht")
	}
	if len(text) > 500 {
		text = text[:500]
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return Message{}, ErrOffline
	}
	room := c.roomLocked(channel)
	if why := room.Muted(); why != "" {
		return Message{}, &MutedError{Reason: why}
	}
	now := time.Now()
	keep := c.sent[:0]
	for _, t := range c.sent {
		if now.Sub(t) < 30*time.Second {
			keep = append(keep, t)
		}
	}
	c.sent = keep
	if len(c.sent) >= 18 {
		return Message{}, ErrRate
	}
	at := now
	prev, had := c.last[channel]
	if !room.Exempt() && had {
		if room.Slow > 0 {
			if next := prev.at.Add(time.Duration(room.Slow)*time.Second + 300*time.Millisecond); next.After(at) {
				at = next
			}
		}
		if (room.Unique || at.Sub(prev.at) < 30*time.Second) && sameText(prev.text, text) {
			text = toggleUnique(prev.text, text)
		}
	}
	if at.Sub(now) > 2*time.Minute {
		return Message{}, errors.New("Slow-Modus: der Bot müsste zu lange warten")
	}
	c.nextID++
	m := Message{ID: fmt.Sprintf("self-%d-%d", now.UnixMilli(), c.nextID), Channel: channel, Login: c.login, User: c.login,
		Text: text, Self: true, Kind: "chat", Time: at}
	if w := at.Sub(now); w > 0 {
		m.Wait = int(w.Round(time.Second) / time.Second)
		if m.Wait == 0 {
			m.Wait = 1
		}
	}
	c.sent = append(c.sent, at)
	c.last[channel] = sentLine{id: m.ID, text: text, at: at}
	c.lastMsg[channel] = m
	line := fmt.Sprintf("PRIVMSG #%s :%s", channel, text)
	if at.After(now) {
		conn := c.conn
		time.AfterFunc(at.Sub(now), func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.conn == conn {
				c.writeLocked(line)
			}
		})
		return m, nil
	}
	if err := c.writeLocked(line); err != nil {
		return Message{}, err
	}
	return m, nil
}

// MutedError is returned when the channel's settings let the bot not write at all.
type MutedError struct{ Reason string }

func (e *MutedError) Error() string { return "der Bot ist hier stumm: " + e.Reason }

// invisible makes a repeated text unique for Twitch without changing what people see.
const invisible = " \U000E0000"

func sameText(a, b string) bool {
	return strings.TrimSuffix(a, invisible) == strings.TrimSuffix(b, invisible)
}

// toggleUnique gives the new text the marker when the old one had none, and the other way round.
func toggleUnique(prev, text string) string {
	text = strings.TrimSuffix(text, invisible)
	if strings.HasSuffix(prev, invisible) {
		return text
	}
	return text + invisible
}

// Room returns what the client knows of a channel's chat settings.
func (c *Client) Room(channel string) Room {
	c.mu.Lock()
	defer c.mu.Unlock()
	return *c.roomLocked(strings.ToLower(strings.TrimPrefix(channel, "#")))
}

// MarkLinksBlocked notes that the channel refuses links from the bot.
func (c *Client) MarkLinksBlocked(channel string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.roomLocked(channel).LinksBlocked = true
}

func (c *Client) roomLocked(channel string) *Room {
	if c.rooms == nil {
		c.rooms = map[string]*Room{}
	}
	if c.last == nil {
		c.last = map[string]sentLine{}
	}
	if c.lastMsg == nil {
		c.lastMsg = map[string]Message{}
	}
	r := c.rooms[channel]
	if r == nil {
		r = &Room{FollowersOnly: -1}
		c.rooms[channel] = r
	}
	return r
}

// Reconnect drops the connection, so the next one uses a fresh token.
func (c *Client) Reconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		c.conn.Close()
	}
	if c.kick != nil {
		select {
		case c.kick <- struct{}{}:
		default:
		}
	}
}

func (c *Client) writeLocked(line string) error {
	if c.conn == nil {
		return ErrOffline
	}
	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := c.conn.Write([]byte(line + "\r\n"))
	return err
}

func (c *Client) state(connected bool, detail string) {
	c.mu.Lock()
	c.connected = connected
	c.mu.Unlock()
	if c.OnState != nil {
		c.OnState(connected, detail)
	}
}

// Run keeps a connection until ctx ends.
func (c *Client) Run(ctx context.Context) {
	c.mu.Lock()
	c.kick = make(chan struct{}, 1)
	c.mu.Unlock()
	wait := 2 * time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		c.state(false, errText(err))
		if time.Since(start) > 2*time.Minute {
			wait = 2 * time.Second
		}
		select {
		case <-time.After(wait):
		case <-c.kick:
			wait = 2 * time.Second
			continue
		case <-ctx.Done():
			return
		}
		if wait < 2*time.Minute {
			wait *= 2
		}
	}
}

func errText(err error) string {
	if err == nil {
		return "getrennt"
	}
	return err.Error()
}

func (c *Client) session(ctx context.Context) error {
	login, token, err := c.Creds(ctx)
	if err != nil {
		return err
	}
	dial := c.Dial
	if dial == nil {
		dial = func(ctx context.Context, addr string) (net.Conn, error) {
			d := tls.Dialer{Config: &tls.Config{MinVersion: tls.VersionTLS12}}
			return d.DialContext(ctx, "tcp", addr)
		}
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	conn, err := dial(dctx, c.Addr)
	cancel()
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	defer conn.Close()

	c.mu.Lock()
	c.conn = conn
	c.login = login
	c.joined = map[string]bool{}
	c.writeLocked("CAP REQ :twitch.tv/tags twitch.tv/commands")
	c.writeLocked("PASS oauth:" + token)
	c.writeLocked("NICK " + login)
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.conn = nil
		c.joined = map[string]bool{}
		c.mu.Unlock()
	}()

	r := bufio.NewReaderSize(conn, 64<<10)
	for {
		conn.SetReadDeadline(time.Now().Add(6 * time.Minute)) // Twitch pings every five
		line, err := r.ReadString('\n')
		if err != nil {
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		if err := c.handle(line); err != nil {
			return err
		}
	}
}

func (c *Client) handle(raw string) error {
	m := parse(raw)
	switch m.command {
	case "PING":
		c.mu.Lock()
		c.writeLocked("PONG :" + m.trailing)
		c.mu.Unlock()
	case "001":
		c.state(true, "verbunden")
		c.mu.Lock()
		want := make([]string, 0, len(c.want))
		for l := range c.want {
			want = append(want, l)
		}
		c.mu.Unlock()
		c.SetChannels(want)
	case "RECONNECT":
		return errors.New("Twitch bittet um eine neue Verbindung")
	case "NOTICE":
		if strings.Contains(m.trailing, "Login authentication failed") || strings.Contains(m.trailing, "Improperly formatted auth") {
			return errors.New("Twitch hat das Token abgelehnt: " + m.trailing)
		}
		ch, id := channelOf(m), m.tags["msg-id"]
		if reason, ok := dropReasons[id]; ok {
			if strings.HasPrefix(id, "msg_followersonly") {
				c.mu.Lock()
				c.roomLocked(ch).NotFollowing = true
				c.mu.Unlock()
			}
			c.dropped(ch, id, reason)
			return nil
		}
		if room, ok := roomNotices[id]; ok {
			c.mu.Lock()
			room(c.roomLocked(ch))
			c.mu.Unlock()
		}
		c.emit(Message{Channel: ch, Text: m.trailing, Kind: "notice", Time: time.Now(), ID: id})
	case "ROOMSTATE":
		c.mu.Lock()
		r := c.roomLocked(channelOf(m))
		r.Known = true
		if v, ok := m.tags["slow"]; ok {
			r.Slow = atoi(v)
		}
		if v, ok := m.tags["followers-only"]; ok {
			if atoi(v) != r.FollowersOnly {
				r.NotFollowing = false // try again under the new rule
			}
			r.FollowersOnly = atoi(v)
		}
		if v, ok := m.tags["subs-only"]; ok {
			r.SubsOnly = v == "1"
		}
		if v, ok := m.tags["emote-only"]; ok {
			r.EmoteOnly = v == "1"
		}
		if v, ok := m.tags["r9k"]; ok {
			r.Unique = v == "1"
		}
		c.mu.Unlock()
		c.emit(Message{Channel: channelOf(m), Kind: "room", Time: time.Now()})
	case "USERSTATE":
		badges := m.tags["badges"]
		c.mu.Lock()
		r := c.roomLocked(channelOf(m))
		r.Mod = m.tags["mod"] == "1" || strings.Contains(badges, "broadcaster/") || strings.Contains(badges, "moderator/")
		r.VIP = strings.Contains(badges, "vip/")
		c.mu.Unlock()
	case "PRIVMSG":
		text, kind := m.trailing, "chat"
		if strings.HasPrefix(text, "\x01ACTION ") {
			text, kind = strings.TrimSuffix(strings.TrimPrefix(text, "\x01ACTION "), "\x01"), "action"
		}
		login := m.nick()
		name := m.tags["display-name"]
		if name == "" {
			name = login
		}
		badges := m.tags["badges"]
		c.emit(Message{
			ID: m.tags["id"], Channel: channelOf(m), Login: login, User: name, Text: text, Color: m.tags["color"],
			Mod: m.tags["mod"] == "1", Broadcaster: strings.Contains(badges, "broadcaster/"),
			VIP: strings.Contains(badges, "vip/"), Sub: m.tags["subscriber"] == "1", Kind: kind, Time: time.Now(),
		})
	case "USERNOTICE":
		text := m.tags["system-msg"]
		if m.trailing != "" {
			text += " " + m.trailing
		}
		c.emit(Message{ID: m.tags["id"], Channel: channelOf(m), Login: m.tags["login"], User: m.tags["display-name"], Text: strings.TrimSpace(text), Kind: "event", Time: time.Now()})
	}
	return nil
}

// dropped tells about the bot's last message in the channel, which Twitch refused.
func (c *Client) dropped(channel, msgID, reason string) {
	c.mu.Lock()
	sent, ok := c.lastMsg[channel]
	if ok {
		delete(c.lastMsg, channel)
		if l, had := c.last[channel]; had && l.id == sent.ID {
			delete(c.last, channel) // it did not count for the slow mode or as the last text
		}
	}
	c.mu.Unlock()
	c.emit(Message{Channel: channel, Kind: "dropped", Ref: sent.ID, Reason: reason, Time: time.Now()})
	if ok && c.OnDropped != nil {
		c.OnDropped(sent, msgID, reason)
	}
}

// dropReasons are Twitch's msg-ids for a message it did not post, with the reason in German.
var dropReasons = map[string]string{
	"msg_slowmode":                       "Slow-Modus: zu schnell nach der letzten Nachricht",
	"msg_duplicate":                      "gleiche Nachricht innerhalb von 30 Sekunden",
	"msg_r9k":                            "Unique-Chat: diese Nachricht gab es schon",
	"msg_followersonly":                  "nur Follower dürfen schreiben",
	"msg_followersonly_followed":         "nur Follower, die schon länger folgen, dürfen schreiben",
	"msg_followersonly_zero":             "nur Follower dürfen schreiben",
	"msg_subsonly":                       "nur Subs dürfen schreiben",
	"msg_emoteonly":                      "nur Emotes erlaubt",
	"msg_ratelimit":                      "zu viele Nachrichten in kurzer Zeit",
	"msg_requires_verified_phone_number": "der Kanal verlangt eine bestätigte Telefonnummer",
	"msg_verified_email":                 "der Kanal verlangt eine bestätigte E-Mail-Adresse",
	"msg_rejected":                       "von den Moderationsregeln des Kanals zurückgehalten",
	"msg_rejected_mandatory":             "von den Moderationsregeln des Kanals abgelehnt",
	"msg_banned":                         "der Bot ist in diesem Kanal gebannt",
	"msg_timedout":                       "der Bot hat gerade einen Timeout",
	"msg_channel_suspended":              "der Kanal ist gesperrt",
	"msg_suspended":                      "das Bot-Konto ist gesperrt",
	"msg_channel_blocked":                "der Kanal blockiert den Bot",
	"msg_banned_phone_number_alias":      "die Telefonnummer des Bots ist dort gebannt",
}

// roomNotices are notices that change what the client knows of a room.
var roomNotices = map[string]func(*Room){
	"slow_off":       func(r *Room) { r.Slow = 0 },
	"followers_off":  func(r *Room) { r.FollowersOnly, r.NotFollowing = -1, false },
	"subs_on":        func(r *Room) { r.SubsOnly = true },
	"subs_off":       func(r *Room) { r.SubsOnly = false },
	"emote_only_on":  func(r *Room) { r.EmoteOnly = true },
	"emote_only_off": func(r *Room) { r.EmoteOnly = false },
	"r9k_on":         func(r *Room) { r.Unique = true },
	"r9k_off":        func(r *Room) { r.Unique = false },
}

func atoi(s string) int {
	n, neg := 0, false
	for i, ch := range s {
		if i == 0 && ch == '-' {
			neg = true
			continue
		}
		if ch < '0' || ch > '9' {
			break
		}
		n = n*10 + int(ch-'0')
	}
	if neg {
		return -n
	}
	return n
}

func (c *Client) emit(m Message) {
	if c.OnMessage != nil {
		c.OnMessage(m)
	}
}

type line struct {
	tags     map[string]string
	prefix   string
	command  string
	params   []string
	trailing string
}

func (l line) nick() string {
	if i := strings.IndexByte(l.prefix, '!'); i > 0 {
		return l.prefix[:i]
	}
	return l.prefix
}

func channelOf(l line) string {
	for _, p := range l.params {
		if strings.HasPrefix(p, "#") {
			return strings.TrimPrefix(p, "#")
		}
	}
	return ""
}

var tagEscapes = strings.NewReplacer(`\s`, " ", `\:`, ";", `\\`, `\`, `\r`, "\r", `\n`, "\n")

func parse(raw string) line {
	var l line
	l.tags = map[string]string{}
	if strings.HasPrefix(raw, "@") {
		tags, rest, _ := strings.Cut(raw[1:], " ")
		for _, kv := range strings.Split(tags, ";") {
			k, v, _ := strings.Cut(kv, "=")
			l.tags[k] = tagEscapes.Replace(v)
		}
		raw = rest
	}
	if strings.HasPrefix(raw, ":") {
		l.prefix, raw, _ = strings.Cut(raw[1:], " ")
	}
	if head, trail, ok := strings.Cut(raw, " :"); ok {
		raw, l.trailing = head, trail
	}
	f := strings.Fields(raw)
	if len(f) > 0 {
		l.command, l.params = f[0], f[1:]
	}
	return l
}

func batch(s []string, n int) [][]string {
	var out [][]string
	for len(s) > 0 {
		k := min(n, len(s))
		out = append(out, s[:k])
		s = s[k:]
	}
	return out
}
