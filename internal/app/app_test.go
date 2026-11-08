package app

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronwerke/twitch/internal/store"
)

func TestCountdown(t *testing.T) {
	start := time.Date(2027, 1, 16, 18, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		now  time.Time
		want string
	}{
		{start.Add(-(69*24 + 3) * time.Hour), "noch 69 Tage und 3 Stunden"},
		{start.Add(-25 * time.Hour), "noch 1 Tag und 1 Stunde"},
		{start.Add(-90 * time.Minute), "noch 1 Stunde und 30 Minuten"},
		{start.Add(-20 * time.Second), "noch 1 Minute"},
		{start.Add(time.Hour), "Season 2 läuft"},
	} {
		if got := countdown(c.now, start); got != c.want {
			t.Errorf("%v: got %q, want %q", c.now, got, c.want)
		}
	}
	if got := germanDate(start); got != "Samstag, 16. Januar 2027, um 18 Uhr" {
		t.Errorf("date %q", got)
	}
}

// fakeTwitch is Twitch's OAuth and Helix: every code is a token of the account named in
// it ("code-<login>-<id>").
func fakeTwitch(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-" + strings.TrimPrefix(r.Form.Get("code"), "code-"), "refresh_token": "ref", "expires_in": 3600})
		case "client_credentials":
			json.NewEncoder(w).Encode(map[string]any{"access_token": "app", "expires_in": 3600})
		case "refresh_token":
			json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-renewed", "refresh_token": "ref2", "expires_in": 3600})
		}
	})
	mux.HandleFunc("GET /oauth2/validate", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "OAuth tok-"), "-")
		json.NewEncoder(w).Encode(map[string]any{"login": parts[0], "user_id": parts[len(parts)-1], "scopes": []string{"chat:read"}})
	})
	mux.HandleFunc("GET /helix/users", func(w http.ResponseWriter, r *http.Request) {
		var out []map[string]string
		for _, l := range r.URL.Query()["login"] {
			out = append(out, map[string]string{"id": "9" + l, "login": l, "display_name": strings.ToUpper(l[:1]) + l[1:]})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": out})
	})
	mux.HandleFunc("GET /helix/streams", func(w http.ResponseWriter, r *http.Request) {
		var out []map[string]any
		for _, l := range r.URL.Query()["user_login"] {
			if l == "carla" {
				out = append(out, map[string]any{"user_login": l, "user_name": "Carla", "title": "Kronwerke Tag 1", "game_name": "Minecraft", "viewer_count": 42, "thumbnail_url": "https://x/{width}x{height}.jpg"})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": out})
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// fakeChat is Twitch's IRC: it records what the bot writes and can say things.
type fakeChat struct {
	mu    sync.Mutex
	lines []string
	conn  net.Conn
	ready chan struct{}
}

func (f *fakeChat) dial(ctx context.Context, addr string) (net.Conn, error) {
	server, client := net.Pipe()
	f.mu.Lock()
	f.conn = server
	f.mu.Unlock()
	go func() {
		r := bufio.NewReader(server)
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				return
			}
			l = strings.TrimSpace(l)
			f.mu.Lock()
			f.lines = append(f.lines, l)
			f.mu.Unlock()
			if strings.HasPrefix(l, "NICK ") {
				io.WriteString(server, ":tmi.twitch.tv 001 "+strings.TrimPrefix(l, "NICK ")+" :Welcome, GLHF!\r\n")
				close(f.ready)
			}
		}
	}()
	return client, nil
}

func (f *fakeChat) say(channel, login, text string, mod bool) {
	m := "0"
	if mod {
		m = "1"
	}
	f.mu.Lock()
	c := f.conn
	f.mu.Unlock()
	io.WriteString(c, "@badges=;color=#FF0000;display-name="+login+";id=x;mod="+m+";subscriber=0 :"+login+"!"+login+"@"+login+".tmi.twitch.tv PRIVMSG #"+channel+" :"+text+"\r\n")
}

func (f *fakeChat) wait(t *testing.T, what string, ok func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, l := range f.lines {
			if ok(l) {
				f.mu.Unlock()
				return l
			}
		}
		f.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t.Fatalf("%s: never seen in %q", what, f.lines)
	return ""
}

func newTestApp(t *testing.T) (*App, *httptest.Server, *fakeChat) {
	tw := fakeTwitch(t)
	a, err := New(Config{ClientID: "cid", ClientSecret: "sec", Owner: "elchi_dev", AdminToken: strings.Repeat("a", 40),
		StatePath: filepath.Join(t.TempDir(), "state.json"), Version: "v0.0.0-test"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	a.tw.Auth, a.tw.Helix = tw.URL, tw.URL+"/helix"
	chat := &fakeChat{ready: make(chan struct{})}
	a.chat.Dial = chat.dial
	srv := httptest.NewTLSServer(a.routes())
	t.Cleanup(srv.Close)
	a.cfg.PublicURL = srv.URL
	return a, srv, chat
}

// signIn walks the OAuth redirect by hand: /auth/login, then the callback with a code.
func signIn(t *testing.T, srv *httptest.Server, code string) (*http.Client, *http.Response) {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Transport: srv.Client().Transport, Jar: jar}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r, err := c.Get(srv.URL + "/auth/login")
	if err != nil || r.StatusCode != http.StatusFound {
		t.Fatalf("login: %v %v", err, r)
	}
	loc, _ := url.Parse(r.Header.Get("Location"))
	state := loc.Query().Get("state")
	r, err = c.Get(srv.URL + "/auth/callback?code=" + code + "&state=" + state)
	if err != nil {
		t.Fatal(err)
	}
	return c, r
}

func TestOwnerSignsInAndIsBoundToTheTwitchID(t *testing.T) {
	a, srv, _ := newTestApp(t)
	c, r := signIn(t, srv, "code-elchi_dev-111")
	if r.StatusCode != http.StatusFound || r.Header.Get("Location") != "/" {
		t.Fatalf("owner refused: %d", r.StatusCode)
	}
	r, _ = c.Get(srv.URL + "/api/me")
	var me map[string]any
	json.NewDecoder(r.Body).Decode(&me)
	if me["login"] != "elchi_dev" || me["csrf"] == "" {
		t.Fatalf("me %v", me)
	}
	var id string
	a.store.View(func(d *store.Data) { id = d.Users[0].ID })
	if id != "111" {
		t.Fatalf("not bound: %q", id)
	}
	// someone who takes the name later gets nothing
	_, r = signIn(t, srv, "code-elchi_dev-222")
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("other account with the same name: %d", r.StatusCode)
	}
	_, r = signIn(t, srv, "code-random-333")
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("stranger: %d", r.StatusCode)
	}
	// writes need the CSRF token
	req, _ := http.NewRequest("POST", srv.URL+"/api/channels", strings.NewReader(`{"login":"carla"}`))
	r, _ = c.Do(req)
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("write without csrf: %d", r.StatusCode)
	}
	req, _ = http.NewRequest("POST", srv.URL+"/api/channels", strings.NewReader(`{"login":"carla"}`))
	req.Header.Set("X-Csrf-Token", me["csrf"].(string))
	r, _ = c.Do(req)
	if r.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("add channel: %d %s", r.StatusCode, b)
	}
}

func TestBotAnswersCommandsAndTheAPIChangesThemAtOnce(t *testing.T) {
	a, srv, chat := newTestApp(t)
	c, _ := signIn(t, srv, "code-elchi_dev-111")
	// connect the bot account
	r, _ := c.Get(srv.URL + "/auth/bot")
	loc, _ := url.Parse(r.Header.Get("Location"))
	if !strings.Contains(loc.Query().Get("scope"), "chat:edit") || loc.Query().Get("force_verify") != "true" {
		t.Fatalf("bot authorize url %s", loc)
	}
	r, _ = c.Get(srv.URL + "/auth/callback?code=code-kronwerkebot-500&state=" + loc.Query().Get("state"))
	if r.Header.Get("Location") != "/#bot" {
		t.Fatalf("bot callback %d %s", r.StatusCode, r.Header.Get("Location"))
	}

	api := func(method, path, body string) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 40))
		r, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := api("POST", "/api/channels", `{"login":"https://www.twitch.tv/Carla"}`); r.StatusCode != 204 {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("add channel %d %s", r.StatusCode, b)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go a.chat.Run(ctx)
	<-chat.ready
	chat.wait(t, "pass", func(l string) bool { return l == "PASS oauth:tok-kronwerkebot-500" })
	chat.wait(t, "join", func(l string) bool { return l == "JOIN #carla" })

	chat.say("carla", "viewer", "!start", false)
	chat.wait(t, "answer", func(l string) bool {
		return strings.HasPrefix(l, "PRIVMSG #carla :Season 2 startet am Samstag, 16. Januar 2027, um 18 Uhr, noch ")
	})

	// a new command from the API works without a restart
	if r := api("PUT", "/api/commands/pack", `{"response":"Pack für {user}: {website}/installieren.html","enabled":true,"cooldown":0}`); r.StatusCode != 200 {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("put command %d %s", r.StatusCode, b)
	}
	chat.say("carla", "Anna", "!pack", false)
	chat.wait(t, "new command", func(l string) bool {
		return l == "PRIVMSG #carla :Pack für Anna: https://kronwerke.com/installieren.html"
	})

	// mod only commands stay quiet for viewers
	api("PUT", "/api/commands/geheim", `{"response":"nur mods","enabled":true,"mod_only":true}`)
	chat.say("carla", "viewer", "!geheim", false)
	chat.say("carla", "mod", "!geheim", true)
	chat.wait(t, "mod command", func(l string) bool { return l == "PRIVMSG #carla :nur mods" })

	// the live list for the website
	a.refreshLive(ctx)
	r = api("GET", "/api/live", "")
	if r.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("no CORS on /api/live")
	}
	var live struct {
		Live []LiveEntry `json:"live"`
	}
	json.NewDecoder(r.Body).Decode(&live)
	if len(live.Live) != 1 || live.Live[0].Name != "Carla" || live.Live[0].Thumbnail != "https://x/640x360.jpg" {
		t.Fatalf("live %+v", live)
	}

	// turning the channel off makes the bot leave it
	api("PATCH", "/api/channels/carla", `{"enabled":false}`)
	chat.wait(t, "part", func(l string) bool { return l == "PART #carla" })
}

func TestSignedOutAPIIsClosed(t *testing.T) {
	_, srv, _ := newTestApp(t)
	for _, p := range []string{"/api/overview", "/api/chat", "/api/me"} {
		r, _ := srv.Client().Get(srv.URL + p)
		if r.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %d", p, r.StatusCode)
		}
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/overview", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	r, _ := srv.Client().Do(req)
	if r.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", r.StatusCode)
	}
}

func TestOverviewHasNoNullLists(t *testing.T) {
	a, _, _ := newTestApp(t)
	b, _ := json.Marshal(a.overview())
	for _, k := range []string{`"channels":null`, `"commands":null`, `"timers":null`, `"users":null`, `"joined":null`} {
		if strings.Contains(string(b), k) {
			t.Errorf("%s in %s", k, b)
		}
	}
}
