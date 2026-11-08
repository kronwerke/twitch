package app

import (
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kronwerke/twitch/internal/irc"
	"github.com/kronwerke/twitch/internal/store"
)

// TestDashboardForScreenshots keeps a dashboard with sample data running for a while, to
// look at it in a browser: SHOT=1 go test -run Screenshots ./internal/app -v
func TestDashboardForScreenshots(t *testing.T) {
	if os.Getenv("SHOT") == "" {
		t.Skip("SHOT not set")
	}
	a, _, _ := newTestApp(t)
	a.store.Update(func(d *store.Data) error {
		d.Users[0].ID = "111"
		d.Bot = &store.Bot{Login: "kronwerkebot", Name: "KronwerkeBot", Linked: time.Now(), By: "elchi_dev", Expires: time.Now().Add(time.Hour)}
		for _, l := range []string{"carla", "menon", "novo", "itzamnix", "twext_0"} {
			d.Channels = append(d.Channels, store.Channel{Login: l, Name: strings.ToUpper(l[:1]) + l[1:], Enabled: true, Website: true, Added: time.Now()})
		}
		d.Sessions[store.Hash("shot")] = store.Session{UserID: "111", Login: "elchi_dev", CSRF: "c", Expires: time.Now().Add(time.Hour)}
		return nil
	})
	a.refreshLive(t.Context())
	lines := []irc.Message{
		{Channel: "carla", User: "Anna", Text: "wann geht kronwerke los?", Color: "#8bb6dc"},
		{Channel: "carla", User: "KronwerkeBot", Text: "Season 2 startet am Samstag, 16. Januar 2027, um 18 Uhr, noch 68 Tage und 23 Stunden.", Self: true},
		{Channel: "menon", User: "Menon", Text: "heute bauen wir die erste Mühle", Broadcaster: true, Color: "#e5b451"},
		{Channel: "novo", User: "Ben", Text: "!discord", Color: "#8fd18b"},
		{Channel: "novo", User: "KronwerkeBot", Text: "Der Kronwerke-Discord: https://discord.gg/DZs3XyyHDj Dort holst du dir auch deinen Platz auf der Whitelist.", Self: true},
		{Channel: "itzamnix", Text: "Itzamnix hat den Slow-Modus aktiviert.", Kind: "notice"},
	}
	for _, m := range lines {
		if m.Kind == "" {
			m.Kind = "chat"
		}
		m.Time = time.Now()
		a.hub.add(m)
	}
	srv := httptest.NewUnstartedServer(a.routes())
	srv.StartTLS()
	defer srv.Close()
	fmt.Println("DASHBOARD", srv.URL)
	time.Sleep(90 * time.Second)
}
