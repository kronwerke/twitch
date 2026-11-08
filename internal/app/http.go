package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kronwerke/twitch/internal/store"
)

//go:embed web
var webFS embed.FS

const sessionCookie = "__Host-ttv"
const stateCookie = "__Host-ttv-state"
const sessionLife = 30 * 24 * time.Hour

var loginRe = regexp.MustCompile(`^[a-z0-9_]{3,25}$`)
var commandRe = regexp.MustCompile(`^[a-z0-9_äöü]{1,30}$`)

// actor is who makes a request: a signed in user or the API token.
type actor struct {
	Login, ID string
	API       bool
	CSRF      string
}

type actorKey struct{}

func (a *App) serveHTTP(ctx context.Context) {
	srv := &http.Server{Addr: a.cfg.HTTPAddr, Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}()
	a.log.Info("http", "addr", a.cfg.HTTPAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		a.log.Error("http", "err", err)
	}
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	files := http.FileServerFS(static)
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https://static-cdn.jtvnw.net data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self' https://id.twitch.tv")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(r.URL.Path, "/fonts/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /api/live", a.apiLive)
	mux.HandleFunc("OPTIONS /api/live", a.apiLive)

	mux.HandleFunc("GET /auth/login", a.authLogin)
	mux.HandleFunc("GET /auth/callback", a.authCallback)
	mux.HandleFunc("POST /auth/logout", a.authLogout)
	mux.Handle("GET /auth/bot", a.signedIn(http.HandlerFunc(a.authBot)))

	api := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, a.signedIn(h)) }
	api("GET /api/me", a.apiMe)
	api("GET /api/overview", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, a.overview()) })
	api("GET /api/log", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, a.events.last(200)) })
	api("GET /api/chat", a.apiChat)
	api("POST /api/chat", a.apiSay)
	api("GET /api/events", a.apiEvents)
	api("POST /api/channels", a.apiAddChannel)
	api("PATCH /api/channels/{login}", a.apiEditChannel)
	api("DELETE /api/channels/{login}", a.apiDeleteChannel)
	api("PUT /api/commands/{name}", a.apiPutCommand)
	api("DELETE /api/commands/{name}", a.apiDeleteCommand)
	api("PUT /api/timers/{id}", a.apiPutTimer)
	api("DELETE /api/timers/{id}", a.apiDeleteTimer)
	api("PATCH /api/settings", a.apiSettings)
	api("POST /api/users", a.apiAddUser)
	api("DELETE /api/users/{login}", a.apiDeleteUser)
	api("DELETE /api/bot", a.apiDropBot)
	api("POST /api/preview", a.apiPreview)
	return mux
}

// ---- auth ----

type pending struct {
	kind    string // login or bot
	by      string // for bot: the user who connects it
	expires time.Time
}

var states = struct {
	sync.Mutex
	m map[string]pending
}{m: map[string]pending{}}

func random(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (a *App) startOAuth(w http.ResponseWriter, r *http.Request, p pending, scopes []string, force bool) {
	if !a.tw.Configured() {
		http.Error(w, "Die Twitch-Anwendung fehlt: TTV_CLIENT_ID und TTV_CLIENT_SECRET in twitch.env eintragen.", http.StatusServiceUnavailable)
		return
	}
	st := random(16)
	p.expires = time.Now().Add(10 * time.Minute)
	states.Lock()
	for k, v := range states.m {
		if time.Now().After(v.expires) {
			delete(states.m, k)
		}
	}
	states.m[st] = p
	states.Unlock()
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: st, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	http.Redirect(w, r, a.tw.AuthorizeURL(a.callbackURL(), st, scopes, force), http.StatusFound)
}

func (a *App) authLogin(w http.ResponseWriter, r *http.Request) {
	a.startOAuth(w, r, pending{kind: "login"}, nil, false)
}

func (a *App) authBot(w http.ResponseWriter, r *http.Request) {
	act := r.Context().Value(actorKey{}).(actor)
	a.startOAuth(w, r, pending{kind: "bot", by: act.Login}, BotScopes, true)
}

func (a *App) authCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, err := r.Cookie(stateCookie)
	states.Lock()
	p, ok := states.m[q.Get("state")]
	delete(states.m, q.Get("state"))
	states.Unlock()
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: "", Path: "/", Secure: true, HttpOnly: true, MaxAge: -1})
	if err != nil || !ok || c.Value != q.Get("state") || time.Now().After(p.expires) {
		page(w, http.StatusBadRequest, "Die Anmeldung ist abgelaufen oder kam nicht von hier. Bitte noch einmal versuchen.")
		return
	}
	if e := q.Get("error"); e != "" {
		page(w, http.StatusBadRequest, "Twitch hat die Anmeldung nicht bestätigt ("+q.Get("error_description")+").")
		return
	}
	tok, err := a.tw.Exchange(r.Context(), q.Get("code"), a.callbackURL())
	if err != nil {
		a.events.add("oauth exchange: %v", err)
		page(w, http.StatusBadGateway, "Twitch hat den Code nicht angenommen. Bitte noch einmal versuchen.")
		return
	}
	id, err := a.tw.Validate(r.Context(), tok.Access)
	if err != nil {
		page(w, http.StatusBadGateway, "Twitch hat das Konto nicht bestätigt. Bitte noch einmal versuchen.")
		return
	}
	switch p.kind {
	case "login":
		a.finishLogin(w, r, id.Login, id.UserID)
	case "bot":
		err := a.store.Update(func(d *store.Data) error {
			d.Bot = &store.Bot{ID: id.UserID, Login: id.Login, Name: id.Login, Access: tok.Access, Refresh: tok.Refresh,
				Expires: time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second), Scopes: id.Scopes, Linked: time.Now(), By: p.by}
			return nil
		})
		if err != nil {
			page(w, 500, "Speichern ging nicht: "+err.Error())
			return
		}
		a.events.add("bot account %s connected by %s", id.Login, p.by)
		a.chat.Reconnect()
		http.Redirect(w, r, "/#bot", http.StatusFound)
	}
}

func (a *App) finishLogin(w http.ResponseWriter, r *http.Request, login, userID string) {
	login = store.Lower(login)
	var allowed bool
	err := a.store.Update(func(d *store.Data) error {
		for i := range d.Users {
			u := &d.Users[i]
			if (u.ID != "" && u.ID == userID) || (u.ID == "" && u.Login == login) {
				u.ID, u.Login, u.LastSeen = userID, login, time.Now()
				allowed = true
				return nil
			}
		}
		return nil
	})
	if err != nil || !allowed {
		a.events.add("sign-in refused for %s (%s)", login, userID)
		page(w, http.StatusForbidden, "Das Twitch-Konto "+login+" hat hier keinen Zugang.")
		return
	}
	token := random(32)
	csrf := random(16)
	a.store.Update(func(d *store.Data) error {
		d.Sessions[store.Hash(token)] = store.Session{UserID: userID, Login: login, CSRF: csrf, Expires: time.Now().Add(sessionLife)}
		return nil
	})
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(sessionLife.Seconds())})
	a.events.add("%s signed in", login)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (a *App) authLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.store.Update(func(d *store.Data) error { delete(d.Sessions, store.Hash(c.Value)); return nil })
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", Secure: true, HttpOnly: true, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

// signedIn lets a request through with a session (and, when it changes something, its
// CSRF token) or with the API token.
func (a *App) signedIn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") && a.cfg.AdminToken != "" {
			if subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(h, "Bearer ")), []byte(a.cfg.AdminToken)) == 1 {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, actor{Login: "api", API: true})))
				return
			}
			writeErr(w, http.StatusUnauthorized, "falsches Token")
			return
		}
		c, err := r.Cookie(sessionCookie)
		var s store.Session
		ok := false
		if err == nil {
			a.store.View(func(d *store.Data) {
				s, ok = d.Sessions[store.Hash(c.Value)]
				if ok {
					// the user must still be on the list
					found := false
					for _, u := range d.Users {
						if u.ID == s.UserID {
							found = true
						}
					}
					ok = found
				}
			})
		}
		if !ok || time.Now().After(s.Expires) {
			writeErr(w, http.StatusUnauthorized, "nicht angemeldet")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Csrf-Token")), []byte(s.CSRF)) != 1 {
				writeErr(w, http.StatusForbidden, "CSRF-Token fehlt")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, actor{Login: s.Login, ID: s.UserID, CSRF: s.CSRF})))
	})
}

// ---- API ----

func (a *App) apiMe(w http.ResponseWriter, r *http.Request) {
	act := r.Context().Value(actorKey{}).(actor)
	writeJSON(w, 200, map[string]any{"login": act.Login, "csrf": act.CSRF, "api": act.API})
}

func (a *App) apiLive(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET")
	w.Header().Set("Cache-Control", "public, max-age=30")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	a.liveMu.RLock()
	at := a.liveAt
	a.liveMu.RUnlock()
	writeJSON(w, 200, map[string]any{"live": a.liveNow(), "checked": at, "start": a.start(), "countdown": countdown(time.Now(), a.start())})
}

func (a *App) apiChat(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n <= 0 || n > 600 {
		n = 200
	}
	writeJSON(w, 200, a.hub.recent(n, store.Lower(r.URL.Query().Get("channel"))))
}

func (a *App) apiSay(w http.ResponseWriter, r *http.Request) {
	var in struct{ Channel, Text string }
	if !readJSON(w, r, &in) {
		return
	}
	ch := store.Lower(in.Channel)
	if !contains(a.chat.Joined(), ch) {
		writeErr(w, 400, "der Bot sitzt nicht in diesem Kanal")
		return
	}
	if err := a.Say(ch, a.fill(in.Text, ch, "", "")); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	a.events.add("%s wrote in %s", r.Context().Value(actorKey{}).(actor).Login, ch)
	w.WriteHeader(http.StatusNoContent)
}

// apiEvents is the live stream for the dashboard (server sent events).
func (a *App) apiEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	sub := a.hub.subscribe()
	defer a.hub.unsubscribe(sub)
	every := a.ssePing
	if every <= 0 {
		every = 25 * time.Second
	}
	ping := time.NewTicker(every)
	defer ping.Stop()
	fmt.Fprint(w, "retry: 2000\n: hello\n\n")
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			// a named event: the dashboard sees it and knows the line is alive, while
			// clients that only listen to plain messages are not bothered
			fmt.Fprint(w, "event: ping\ndata: {}\n\n")
			fl.Flush()
		case e := <-sub:
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
	}
}

func (a *App) changed(r *http.Request, what string, args ...any) {
	act := r.Context().Value(actorKey{}).(actor)
	a.events.add("%s: %s", act.Login, fmt.Sprintf(what, args...))
	a.hub.publish(event{Kind: "state", State: a.overview()})
}

func (a *App) apiAddChannel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Login string `json:"login"`
		Note  string `json:"note"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	login := store.Lower(strings.TrimPrefix(strings.TrimPrefix(store.Lower(in.Login), "https://www.twitch.tv/"), "twitch.tv/"))
	if !loginRe.MatchString(login) {
		writeErr(w, 400, "das ist kein Twitch-Name")
		return
	}
	var id, name string
	if a.tw.Configured() {
		users, err := a.tw.Users(r.Context(), []string{login})
		if err != nil {
			writeErr(w, 502, "Twitch: "+err.Error())
			return
		}
		if len(users) == 0 {
			writeErr(w, 404, "diesen Twitch-Kanal gibt es nicht")
			return
		}
		id, name = users[0].ID, users[0].DisplayName
		a.liveMu.Lock()
		a.users[login] = users[0]
		a.liveMu.Unlock()
	}
	err := a.store.Update(func(d *store.Data) error {
		for _, c := range d.Channels {
			if c.Login == login {
				return errors.New("der Kanal ist schon in der Liste")
			}
		}
		d.Channels = append(d.Channels, store.Channel{Login: login, ID: id, Name: name, Enabled: true, Website: true, Note: in.Note, Added: time.Now()})
		return nil
	})
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	a.chat.SetChannels(a.store.EnabledChannels())
	go a.refreshLive(context.Background())
	a.changed(r, "channel %s added", login)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) apiEditChannel(w http.ResponseWriter, r *http.Request) {
	login := store.Lower(r.PathValue("login"))
	var in struct {
		Enabled *bool   `json:"enabled"`
		Website *bool   `json:"website"`
		Note    *string `json:"note"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	err := a.store.Update(func(d *store.Data) error {
		for i := range d.Channels {
			if d.Channels[i].Login == login {
				if in.Enabled != nil {
					d.Channels[i].Enabled = *in.Enabled
				}
				if in.Website != nil {
					d.Channels[i].Website = *in.Website
				}
				if in.Note != nil {
					d.Channels[i].Note = *in.Note
				}
				return nil
			}
		}
		return errors.New("diesen Kanal gibt es nicht")
	})
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	a.chat.SetChannels(a.store.EnabledChannels())
	a.changed(r, "channel %s changed", login)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) apiDeleteChannel(w http.ResponseWriter, r *http.Request) {
	login := store.Lower(r.PathValue("login"))
	a.store.Update(func(d *store.Data) error {
		out := d.Channels[:0]
		for _, c := range d.Channels {
			if c.Login != login {
				out = append(out, c)
			}
		}
		d.Channels = out
		return nil
	})
	a.chat.SetChannels(a.store.EnabledChannels())
	a.changed(r, "channel %s removed", login)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) apiPutCommand(w http.ResponseWriter, r *http.Request) {
	old := strings.ToLower(strings.TrimPrefix(r.PathValue("name"), "!"))
	var c store.Command
	if !readJSON(w, r, &c) {
		return
	}
	c.Name = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(c.Name), "!"))
	if c.Name == "" {
		c.Name = old
	}
	if !commandRe.MatchString(c.Name) {
		writeErr(w, 400, "Command-Namen: Buchstaben, Zahlen und Unterstrich, ohne Leerzeichen")
		return
	}
	c.Response = strings.TrimSpace(c.Response)
	if c.Response == "" || len(c.Response) > 480 {
		writeErr(w, 400, "die Antwort ist leer oder länger als 480 Zeichen")
		return
	}
	var aliases []string
	for _, al := range c.Aliases {
		al = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(al), "!"))
		if al != "" && al != c.Name && commandRe.MatchString(al) {
			aliases = append(aliases, al)
		}
	}
	c.Aliases = aliases
	for i := range c.Channels {
		c.Channels[i] = store.Lower(c.Channels[i])
	}
	c.Cooldown = max(0, min(c.Cooldown, 3600))
	err := a.store.Update(func(d *store.Data) error {
		for _, x := range d.Commands {
			if x.Name == old {
				continue
			}
			if x.Name == c.Name || contains(x.Aliases, c.Name) || contains(c.Aliases, x.Name) {
				return fmt.Errorf("!%s gibt es schon", c.Name)
			}
		}
		for i := range d.Commands {
			if d.Commands[i].Name == old {
				c.Uses = d.Commands[i].Uses
				d.Commands[i] = c
				return nil
			}
		}
		d.Commands = append(d.Commands, c)
		return nil
	})
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	a.changed(r, "command !%s saved", c.Name)
	writeJSON(w, 200, c)
}

func (a *App) apiDeleteCommand(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimPrefix(r.PathValue("name"), "!"))
	a.store.Update(func(d *store.Data) error {
		out := d.Commands[:0]
		for _, c := range d.Commands {
			if c.Name != name {
				out = append(out, c)
			}
		}
		d.Commands = out
		return nil
	})
	a.changed(r, "command !%s removed", name)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) apiPutTimer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var t store.Timer
	if !readJSON(w, r, &t) {
		return
	}
	if id == "new" {
		id = "t" + random(4)
	}
	t.ID = id
	t.Text = strings.TrimSpace(t.Text)
	if t.Text == "" || len(t.Text) > 480 {
		writeErr(w, 400, "der Text ist leer oder länger als 480 Zeichen")
		return
	}
	if t.Minutes < 5 {
		writeErr(w, 400, "ein Timer läuft frühestens alle 5 Minuten")
		return
	}
	t.MinLines = max(0, t.MinLines)
	for i := range t.Channels {
		t.Channels[i] = store.Lower(t.Channels[i])
	}
	a.store.Update(func(d *store.Data) error {
		for i := range d.Timers {
			if d.Timers[i].ID == id {
				d.Timers[i] = t
				return nil
			}
		}
		d.Timers = append(d.Timers, t)
		return nil
	})
	a.changed(r, "timer %s saved", t.Name)
	writeJSON(w, 200, t)
}

func (a *App) apiDeleteTimer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a.store.Update(func(d *store.Data) error {
		out := d.Timers[:0]
		for _, t := range d.Timers {
			if t.ID != id {
				out = append(out, t)
			}
		}
		d.Timers = out
		return nil
	})
	a.changed(r, "timer %s removed", id)
	w.WriteHeader(http.StatusNoContent)
}

var settingKeys = map[string]bool{"start": true, "discord": true, "website": true, "server": true}

func (a *App) apiSettings(w http.ResponseWriter, r *http.Request) {
	var in map[string]string
	if !readJSON(w, r, &in) {
		return
	}
	for k, v := range in {
		if !settingKeys[k] {
			writeErr(w, 400, "unbekannte Einstellung "+k)
			return
		}
		if k == "start" {
			if _, err := time.Parse("2006-01-02T15:04", strings.TrimSpace(v)); err != nil {
				writeErr(w, 400, "Start als 2027-01-16T18:00")
				return
			}
		}
	}
	a.store.Update(func(d *store.Data) error {
		for k, v := range in {
			d.Settings[k] = strings.TrimSpace(v)
		}
		return nil
	})
	a.changed(r, "settings changed")
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) apiAddUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Login string `json:"login"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	login := store.Lower(in.Login)
	if !loginRe.MatchString(login) {
		writeErr(w, 400, "das ist kein Twitch-Name")
		return
	}
	err := a.store.Update(func(d *store.Data) error {
		for _, u := range d.Users {
			if u.Login == login {
				return errors.New("hat schon Zugang")
			}
		}
		d.Users = append(d.Users, store.User{Login: login, Added: time.Now()})
		return nil
	})
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	a.changed(r, "access for %s", login)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) apiDeleteUser(w http.ResponseWriter, r *http.Request) {
	login := store.Lower(r.PathValue("login"))
	act := r.Context().Value(actorKey{}).(actor)
	err := a.store.Update(func(d *store.Data) error {
		out := d.Users[:0]
		for _, u := range d.Users {
			if u.Login == login {
				if u.Owner {
					return errors.New("der Besitzer bleibt")
				}
				if u.ID != "" && u.ID == act.ID {
					return errors.New("den eigenen Zugang nimmst du dir nicht selbst")
				}
				for k, s := range d.Sessions {
					if s.UserID == u.ID {
						delete(d.Sessions, k)
					}
				}
				continue
			}
			out = append(out, u)
		}
		d.Users = out
		return nil
	})
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	a.changed(r, "access for %s removed", login)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) apiDropBot(w http.ResponseWriter, r *http.Request) {
	a.store.Update(func(d *store.Data) error { d.Bot = nil; return nil })
	a.chat.Reconnect()
	a.changed(r, "bot account disconnected")
	w.WriteHeader(http.StatusNoContent)
}

// apiPreview fills the placeholders of a text, for the editor.
func (a *App) apiPreview(w http.ResponseWriter, r *http.Request) {
	var in struct{ Text, Channel string }
	if !readJSON(w, r, &in) {
		return
	}
	ch := store.Lower(in.Channel)
	if ch == "" {
		ch = "elchi_dev"
	}
	writeJSON(w, 200, map[string]string{"text": a.fill(in.Text, ch, "Zuschauer", "")})
}

// ---- helpers ----

func readJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		writeErr(w, 400, "ungültiges JSON: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func page(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Kronwerke Twitch</title>
<link rel="icon" href="/favicon.svg" type="image/svg+xml">
<style>@font-face{font-family:"Schibsted Grotesk";src:url("/fonts/schibsted-grotesk-latin-wght-normal.woff2") format("woff2");font-weight:400 900}
body{margin:0;min-height:100vh;display:grid;place-items:center;background:radial-gradient(600px 380px at 50%% 18%%,rgba(229,180,81,.12),transparent 70%%),#0d0b10;color:#f2ede3;font:15px/1.55 "Schibsted Grotesk",system-ui,sans-serif;-webkit-font-smoothing:antialiased}
main{max-width:24rem;margin:1.5rem;padding:2.5rem 2rem 2rem;border-radius:22px;background:linear-gradient(180deg,rgba(34,29,39,.75),rgba(23,20,27,.9));border:1px solid rgba(242,237,227,.08);text-align:center}
p{margin:0 0 1.5rem;color:#aaa39b}a{display:inline-flex;align-items:center;height:42px;padding:0 18px;border-radius:10px;background:#e5b451;color:#1d1508;font-weight:650;text-decoration:none}a:hover{background:#f0c66c}</style>
<main><p>%s</p><a href="/">Zurück zum Dashboard</a></main>`, htmlEscape(msg))
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}
