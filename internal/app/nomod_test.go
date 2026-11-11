package app

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func (f *fakeChat) raw(line string) {
	f.mu.Lock()
	c := f.conn
	f.mu.Unlock()
	io.WriteString(c, line+"\r\n")
}

func (f *fakeChat) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, l := range f.lines {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// connected gives an app whose bot sits in #carla.
func connected(t *testing.T) (*App, *fakeChat) {
	a, srv, chat := newTestApp(t)
	c, _ := signIn(t, srv, "code-elchi_dev-111")
	r, _ := c.Get(srv.URL + "/auth/bot")
	loc, _ := url.Parse(r.Header.Get("Location"))
	c.Get(srv.URL + "/auth/callback?code=code-kronwerkebot-500&state=" + loc.Query().Get("state"))
	req, _ := http.NewRequest("POST", srv.URL+"/api/channels", strings.NewReader(`{"login":"carla"}`))
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 40))
	if r, err := srv.Client().Do(req); err != nil || r.StatusCode != 204 {
		t.Fatalf("add channel %v %v", err, r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go a.chat.Run(ctx)
	<-chat.ready
	chat.wait(t, "join", func(l string) bool { return l == "JOIN #carla" })
	return a, chat
}

func waitRoom(t *testing.T, a *App, ok func() bool) {
	t.Helper()
	for i := 0; i < 200 && !ok(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !ok() {
		t.Fatalf("room state never arrived: %+v", a.chat.Room("carla"))
	}
}

// Without mod rights the bot keeps the slow mode and makes a repeated text unique.
func TestBotKeepsSlowModeAndAvoidsDuplicates(t *testing.T) {
	a, chat := connected(t)
	chat.raw("@emote-only=0;followers-only=-1;r9k=0;slow=1;subs-only=0 :tmi.twitch.tv ROOMSTATE #carla")
	waitRoom(t, a, func() bool { return a.chat.Room("carla").Slow == 1 })

	if err := a.Say("carla", "Hallo"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := a.Say("carla", "Hallo"); err != nil {
		t.Fatal(err)
	}
	first := chat.wait(t, "first", func(l string) bool { return l == "PRIVMSG #carla :Hallo" })
	_ = first
	if chat.count("PRIVMSG #carla :") != 1 {
		t.Fatal("the second message went out inside the slow mode")
	}
	second := chat.wait(t, "second after the slow mode", func(l string) bool {
		return strings.HasPrefix(l, "PRIVMSG #carla :Hallo") && l != "PRIVMSG #carla :Hallo"
	})
	if time.Since(start) < time.Second {
		t.Fatalf("second message after %v, slow mode is 1 s", time.Since(start))
	}
	if !strings.HasSuffix(second, "\U000E0000") {
		t.Fatalf("repeated text not made unique: %q", second)
	}

	// a moderator is exempt: no waiting
	chat.raw("@badges=moderator/1;mod=1 :tmi.twitch.tv USERSTATE #carla")
	waitRoom(t, a, func() bool { return a.chat.Room("carla").Mod })
	before := chat.count("PRIVMSG #carla :")
	a.Say("carla", "Als Mod")
	chat.wait(t, "mod message at once", func(l string) bool { return l == "PRIVMSG #carla :Als Mod" })
	if chat.count("PRIVMSG #carla :") != before+1 {
		t.Fatal("mod message missing")
	}
}

// In a subs only chat the bot does not write into the void, and the channel says why.
func TestBotIsMutedInSubsOnlyChat(t *testing.T) {
	a, chat := connected(t)
	chat.raw("@emote-only=0;followers-only=-1;r9k=0;slow=0;subs-only=1 :tmi.twitch.tv ROOMSTATE #carla")
	waitRoom(t, a, func() bool { return a.chat.Room("carla").SubsOnly })
	if err := a.Say("carla", "Hallo"); err == nil || !strings.Contains(err.Error(), "Nur Subs") {
		t.Fatalf("say in subs only: %v", err)
	}
	if ov := a.overview(); len(ov.Channels) != 1 || ov.Channels[0].Muted == "" {
		t.Fatalf("channel does not say it is muted: %+v", ov.Channels)
	}
}

// A message Twitch refuses shows as dropped with the reason; a refused link goes out
// once more without a clickable link, and later links in that channel too.
func TestDroppedMessagesAndBlockedLinks(t *testing.T) {
	a, chat := connected(t)
	a.Say("carla", "Discord: https://discord.gg/DZs3XyyHDj")
	chat.wait(t, "link message", func(l string) bool { return l == "PRIVMSG #carla :Discord: https://discord.gg/DZs3XyyHDj" })
	chat.raw("@msg-id=msg_rejected_mandatory :tmi.twitch.tv NOTICE #carla :Your message wasn't posted due to conflicts with the channel's moderation settings.")
	chat.wait(t, "defused resend", func(l string) bool { return l == "PRIVMSG #carla :Discord: discord(.)gg/DZs3XyyHDj" })

	var dropped bool
	for _, m := range a.hub.recent(50, "carla") {
		if m.Kind == "dropped" && m.Ref != "" && strings.Contains(m.Reason, "Moderationsregeln") {
			dropped = true
		}
	}
	if !dropped {
		t.Fatalf("no dropped line in %+v", a.hub.recent(50, "carla"))
	}
	if !a.chat.Room("carla").LinksBlocked {
		t.Fatal("links not marked as blocked")
	}
	a.Say("carla", "Seite: kronwerke.com/installieren")
	chat.wait(t, "later link defused", func(l string) bool { return l == "PRIVMSG #carla :Seite: kronwerke(.)com/installieren" })
}

func TestDefuseLinks(t *testing.T) {
	for in, want := range map[string]string{
		"https://discord.gg/abc":         "discord(.)gg/abc",
		"Join kronwerke.net jetzt":       "Join kronwerke(.)net jetzt",
		"Version 0.3.0, kein Link":       "Version 0.3.0, kein Link",
		"http://www.kronwerke.com/a?b=1": "www(.)kronwerke(.)com/a?b=1",
	} {
		if got := defuseLinks(in); got != want {
			t.Errorf("defuseLinks(%q) = %q, want %q", in, got, want)
		}
	}
}
