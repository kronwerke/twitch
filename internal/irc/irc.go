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
	Kind        string    `json:"kind"` // chat, action, notice, event
	Time        time.Time `json:"time"`
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
	c.sent = append(c.sent, now)
	if err := c.writeLocked(fmt.Sprintf("PRIVMSG #%s :%s", channel, text)); err != nil {
		return Message{}, err
	}
	return Message{Channel: channel, Login: c.login, User: c.login, Text: text, Self: true, Kind: "chat", Time: now}, nil
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
		c.emit(Message{Channel: channelOf(m), Text: m.trailing, Kind: "notice", Time: time.Now(), ID: m.tags["msg-id"]})
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
