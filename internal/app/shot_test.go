package app

import (
	"fmt"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kronwerke/twitch/internal/irc"
	"github.com/kronwerke/twitch/internal/store"
	"github.com/kronwerke/twitch/internal/twitch"
)

// TestDashboardForScreenshots keeps a dashboard with sample data running for a while, to
// look at it in a browser: SHOT=1 go test -run Screenshots ./internal/app -v
//
// SHOT=empty starts without channels and without a bot account. SHOT_ADDR fixes the
// address (to restart on the same port), SHOT_FOR is how many seconds it stays up (90),
// SHOT_CHAT=1 adds a chat line every few seconds.
func TestDashboardForScreenshots(t *testing.T) {
	mode := os.Getenv("SHOT")
	if mode == "" {
		t.Skip("SHOT not set")
	}
	a, _, chat := newTestApp(t)
	a.store.Update(func(d *store.Data) error {
		d.Users[0].ID = "111"
		d.Users[0].LastSeen = time.Now().Add(-3 * time.Minute)
		d.Sessions[store.Hash("shot")] = store.Session{UserID: "111", Login: "elchi_dev", CSRF: "c", Expires: time.Now().Add(time.Hour)}
		if mode == "empty" {
			return nil
		}
		d.Bot = &store.Bot{Login: "kronwerkebot", Name: "KronwerkeBot", Linked: time.Now().Add(-26 * time.Hour), By: "elchi_dev", Expires: time.Now().Add(time.Hour)}
		for _, l := range []string{"carla", "menon", "novo", "itzamnix", "twext_0"} {
			d.Channels = append(d.Channels, store.Channel{Login: l, Name: strings.ToUpper(l[:1]) + l[1:], Enabled: l != "twext_0", Website: l != "itzamnix", Added: time.Now()})
		}
		d.Channels[3].Note = "Mod-Team von Menon"
		d.Users = append(d.Users, store.User{Login: "menon", ID: "222", Added: time.Now().Add(-72 * time.Hour), LastSeen: time.Now().Add(-5 * time.Hour)},
			store.User{Login: "carla", Added: time.Now().Add(-2 * time.Hour)})
		d.Commands[0].Uses = 41
		d.Commands[2].Uses = 128
		d.Commands = append(d.Commands, store.Command{Name: "regeln", Response: "Bitte freundlich bleiben, kein Spam, keine Links ohne Frage. Danke, {user}!", Cooldown: 120, Enabled: true, ModOnly: true, Channels: []string{"menon", "novo"}})
		d.Timers[0].Enabled = true
		d.Timers = append(d.Timers, store.Timer{ID: "t2", Name: "Website", Text: "Alles zu Kronwerke, den Stufen und dem Start: {website}", Minutes: 45, MinLines: 25, Enabled: false})
		return nil
	})
	if mode != "empty" {
		a.refreshLive(t.Context())
		a.liveMu.Lock()
		a.live["menon"] = twitch.Stream{UserLogin: "menon", UserName: "Menon", Game: "Minecraft", Title: "Kronwerke S2 Vorbereitung: wir planen die Mühle", Viewers: 318, StartedAt: time.Now().Add(-134 * time.Minute), Thumbnail: scene(28, 200)}
		a.live["novo"] = twitch.Stream{UserLogin: "novo", UserName: "Novo", Game: "Minecraft", Title: "Redstone bis zum Umfallen", Viewers: 87, StartedAt: time.Now().Add(-38 * time.Minute), Thumbnail: scene(265, 150)}
		for _, l := range []string{"carla", "menon", "novo", "itzamnix", "twext_0"} {
			u := a.users[l]
			u.Login, u.DisplayName = l, strings.ToUpper(l[:1])+l[1:]
			if l != "twext_0" {
				u.Image = face(l)
			}
			a.users[l] = u
		}
		a.liveMu.Unlock()
		// the fake chat server of the tests: the bot connects and sits in the channels
		a.chat.SetChannels(a.store.EnabledChannels())
		go a.chat.Run(t.Context())
		go func() {
			<-chat.ready
			time.Sleep(300 * time.Millisecond)
			chat.raw("@emote-only=0;followers-only=-1;r9k=1;slow=30;subs-only=0 :tmi.twitch.tv ROOMSTATE #menon")
			chat.raw("@emote-only=0;followers-only=0;r9k=0;slow=0;subs-only=0 :tmi.twitch.tv ROOMSTATE #novo")
			chat.raw("@emote-only=0;followers-only=-1;r9k=0;slow=0;subs-only=1 :tmi.twitch.tv ROOMSTATE #itzamnix")
			chat.raw("@emote-only=0;followers-only=-1;r9k=0;slow=0;subs-only=0 :tmi.twitch.tv ROOMSTATE #carla")
			chat.raw("@badges=moderator/1;mod=1 :tmi.twitch.tv USERSTATE #carla")
			a.chat.MarkLinksBlocked("novo")
		}()
	}
	for _, e := range []string{"elchi_dev signed in", "elchi_dev: channel novo added", "update check: Get \"https://api.github.com/repos/kronwerke/twitch/releases/latest\": dial tcp: i/o timeout",
		"elchi_dev: command !regeln saved", "bot token renewed", "elchi_dev: timer Start und Discord saved", "menon signed in", "elchi_dev: settings changed"} {
		a.events.add("%s", e)
	}
	lines := []irc.Message{
		{Channel: "carla", User: "Anna", Login: "anna", Text: "wann geht kronwerke los?", Color: "#8bb6dc"},
		{Channel: "carla", User: "KronwerkeBot", Text: "Season 2 startet am Samstag, 16. Januar 2027, um 18 Uhr, noch 68 Tage und 23 Stunden.", Self: true},
		{Channel: "menon", User: "Menon", Login: "menon", Text: "heute bauen wir die erste Mühle", Broadcaster: true, Color: "#e5b451"},
		{Channel: "menon", User: "Holzwurm", Login: "holzwurm", Text: "die Mühle wird so gut", Color: "#1E90FF"},
		{Channel: "novo", User: "Ben", Login: "ben", Text: "!discord", Color: "#8fd18b"},
		{ID: "self-shot-1", Channel: "novo", User: "KronwerkeBot", Text: "Der Kronwerke-Discord: https://discord.gg/DZs3XyyHDj Dort holst du dir auch deinen Platz auf der Whitelist.", Self: true},
		{Channel: "novo", Kind: "dropped", Ref: "self-shot-1", Reason: "von den Moderationsregeln des Kanals abgelehnt"},
		{Channel: "novo", User: "KronwerkeBot", Text: "Der Kronwerke-Discord: discord(.)gg/DZs3XyyHDj Dort holst du dir auch deinen Platz auf der Whitelist.", Self: true},
		{Channel: "menon", User: "Lea_Baut", Login: "lea_baut", Text: "kann man auch mit Freunden auf einen Server?", Color: "#FF69B4", VIP: true},
		{Channel: "menon", User: "Ruben", Login: "ruben", Text: "@Lea_Baut ja, alle auf einen, das ist ja der Witz", Mod: true, Color: "#00FF7F"},
		{Channel: "itzamnix", Text: "Itzamnix hat den Slow-Modus aktiviert.", Kind: "notice"},
		{Channel: "novo", User: "Pixelkatze", Login: "pixelkatze", Text: "Novo, was ist das für ein Shader?", Color: "#9ACD32"},
		{Channel: "carla", Text: "Raidzug von Novo mit 42 Leuten!", Kind: "event"},
	}
	if mode != "empty" {
		// an earlier hour of chat, so the feed has something to scroll
		who := []irc.Message{{User: "Holzwurm", Login: "holzwurm", Color: "#1E90FF"}, {User: "Anna", Login: "anna", Color: "#8bb6dc"}, {User: "Pixelkatze", Login: "pixelkatze", Color: "#9ACD32"}, {User: "Ben", Login: "ben", Color: "#8fd18b"}}
		said := []string{"hallo zusammen", "wie weit seid ihr mit der Brücke?", "lol", "das sieht richtig gut aus", "welche Stufe ist das gerade?", "gute Nacht euch", "ist das noch Season 1?", "hype"}
		where := []string{"menon", "novo", "carla", "menon"}
		var early []irc.Message
		for i := 0; i < 36; i++ {
			m := who[i%len(who)]
			m.Channel, m.Text = where[(i/2)%len(where)], said[(i*3)%len(said)]
			early = append(early, m)
		}
		lines = append(early, lines...)
	}
	if mode == "empty" {
		lines = nil
	}
	for i, m := range lines {
		if m.Kind == "" {
			m.Kind = "chat"
		}
		m.Time = time.Now().Add(time.Duration(i-len(lines)) * 40 * time.Second)
		a.hub.add(m)
	}
	srv := httptest.NewUnstartedServer(a.routes())
	if addr := os.Getenv("SHOT_ADDR"); addr != "" {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		srv.Listener.Close()
		srv.Listener = l
	}
	srv.StartTLS()
	defer srv.Close()
	fmt.Println("DASHBOARD", srv.URL)
	stay := 90
	if s, err := strconv.Atoi(os.Getenv("SHOT_FOR")); err == nil && s > 0 {
		stay = s
	}
	end := time.After(time.Duration(stay) * time.Second)
	if os.Getenv("SHOT_CHAT") == "" {
		<-end
		return
	}
	more := []irc.Message{
		{Channel: "menon", User: "Holzwurm", Login: "holzwurm", Text: "GG", Color: "#1E90FF"},
		{Channel: "novo", User: "Pixelkatze", Login: "pixelkatze", Text: "der Sonnenuntergang da hinten ist wunderschön", Color: "#9ACD32"},
		{Channel: "carla", User: "Anna", Login: "anna", Text: "!start", Color: "#8bb6dc"},
		{Channel: "menon", User: "Lea_Baut", Login: "lea_baut", Text: "ich bin dabei!", Color: "#FF69B4"},
	}
	tick := time.NewTicker(2500 * time.Millisecond)
	defer tick.Stop()
	for i := 0; ; i++ {
		select {
		case <-end:
			return
		case <-tick.C:
			m := more[i%len(more)]
			m.Kind, m.Time = "chat", time.Now()
			a.hub.add(m)
		}
	}
}

// scene is a sample stream picture: a sky and hills in the given hues.
func scene(sky, hill int) string {
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 640 360"><defs><linearGradient id="s" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="hsl(%d 60%% 28%%)"/><stop offset="1" stop-color="hsl(%d 70%% 62%%)"/></linearGradient></defs><rect width="640" height="360" fill="url(#s)"/><circle cx="470" cy="150" r="46" fill="hsl(40 90%% 75%%)" opacity=".85"/><path d="M0 250 Q120 170 240 230 T480 210 T640 220 V360 H0z" fill="hsl(%d 35%% 30%%)"/><path d="M0 300 Q160 240 320 290 T640 270 V360 H0z" fill="hsl(%d 40%% 20%%)"/><rect x="90" y="250" width="34" height="40" fill="hsl(30 30%% 35%%)"/><path d="M84 252 L107 228 L130 252z" fill="hsl(10 40%% 35%%)"/></svg>`, sky, sky+20, hill, hill+10)
	return "data:image/svg+xml," + url.PathEscape(svg)
}

// face is a sample profile picture.
func face(login string) string {
	h := 0
	for _, c := range login {
		h = (h*31 + int(c)) % 360
	}
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" fill="hsl(%d 45%% 55%%)"/><circle cx="32" cy="26" r="12" fill="hsl(%d 40%% 85%%)"/><path d="M10 64 Q32 30 54 64z" fill="hsl(%d 40%% 85%%)"/></svg>`, h, h, h)
	return "data:image/svg+xml," + url.PathEscape(svg)
}
