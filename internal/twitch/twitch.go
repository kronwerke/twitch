// Package twitch talks to Twitch's OAuth and Helix APIs: signing in, the bot account's
// tokens, and who is live.
package twitch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client holds the application's credentials.
type Client struct {
	ID, Secret string
	Auth       string // https://id.twitch.tv
	Helix      string // https://api.twitch.tv/helix
	HTTP       *http.Client

	mu       sync.Mutex
	app      string
	appUntil time.Time
}

// New makes a client against the real endpoints.
func New(id, secret string) *Client {
	return &Client{ID: id, Secret: secret, Auth: "https://id.twitch.tv", Helix: "https://api.twitch.tv/helix",
		HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// Configured reports whether an application is set.
func (c *Client) Configured() bool { return c.ID != "" && c.Secret != "" }

// Token is what the token endpoint answers.
type Token struct {
	Access    string   `json:"access_token"`
	Refresh   string   `json:"refresh_token"`
	ExpiresIn int      `json:"expires_in"`
	Scope     []string `json:"scope"`
}

// Identity is what /oauth2/validate says about a user token.
type Identity struct {
	ClientID  string   `json:"client_id"`
	Login     string   `json:"login"`
	UserID    string   `json:"user_id"`
	Scopes    []string `json:"scopes"`
	ExpiresIn int      `json:"expires_in"`
}

// AuthorizeURL is where the browser goes to sign in or to connect an account.
func (c *Client) AuthorizeURL(redirect, state string, scopes []string, force bool) string {
	q := url.Values{}
	q.Set("client_id", c.ID)
	q.Set("redirect_uri", redirect)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("state", state)
	if force {
		q.Set("force_verify", "true")
	}
	return c.Auth + "/oauth2/authorize?" + q.Encode()
}

// Exchange turns the code from the redirect into tokens.
func (c *Client) Exchange(ctx context.Context, code, redirect string) (Token, error) {
	return c.token(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}})
}

// Renew gets a fresh access token for a refresh token.
func (c *Client) Renew(ctx context.Context, refresh string) (Token, error) {
	return c.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
}

func (c *Client) token(ctx context.Context, form url.Values) (Token, error) {
	form.Set("client_id", c.ID)
	form.Set("client_secret", c.Secret)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.Auth+"/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var t Token
	err := c.do(req, &t)
	return t, err
}

// Validate asks Twitch whose token this is.
func (c *Client) Validate(ctx context.Context, access string) (Identity, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.Auth+"/oauth2/validate", nil)
	req.Header.Set("Authorization", "OAuth "+access)
	var id Identity
	err := c.do(req, &id)
	return id, err
}

// appToken is the client credentials token for Helix reads.
func (c *Client) appToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.app != "" && time.Now().Before(c.appUntil) {
		return c.app, nil
	}
	t, err := c.token(ctx, url.Values{"grant_type": {"client_credentials"}})
	if err != nil {
		return "", err
	}
	c.app = t.Access
	c.appUntil = time.Now().Add(time.Duration(t.ExpiresIn)*time.Second - 10*time.Minute)
	return c.app, nil
}

// User is a Helix user.
type User struct {
	ID          string `json:"id"`
	Login       string `json:"login"`
	DisplayName string `json:"display_name"`
	Image       string `json:"profile_image_url"`
}

// Stream is a Helix stream: someone is live right now.
type Stream struct {
	UserID    string    `json:"user_id"`
	UserLogin string    `json:"user_login"`
	UserName  string    `json:"user_name"`
	Game      string    `json:"game_name"`
	Title     string    `json:"title"`
	Viewers   int       `json:"viewer_count"`
	StartedAt time.Time `json:"started_at"`
	Thumbnail string    `json:"thumbnail_url"`
	Tags      []string  `json:"tags"`
}

// Users looks logins up, at most 100 at a time.
func (c *Client) Users(ctx context.Context, logins []string) ([]User, error) {
	var out []User
	for _, part := range batches(logins, 100) {
		var r struct {
			Data []User `json:"data"`
		}
		if err := c.helix(ctx, "/users", "login", part, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Data...)
	}
	return out, nil
}

// Streams returns those of the logins who are live.
func (c *Client) Streams(ctx context.Context, logins []string) ([]Stream, error) {
	var out []Stream
	for _, part := range batches(logins, 100) {
		var r struct {
			Data []Stream `json:"data"`
		}
		if err := c.helix(ctx, "/streams", "user_login", part, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Data...)
	}
	return out, nil
}

func (c *Client) helix(ctx context.Context, path, key string, values []string, into any) error {
	tok, err := c.appToken(ctx)
	if err != nil {
		return err
	}
	q := url.Values{}
	for _, v := range values {
		q.Add(key, v)
	}
	if key == "user_login" {
		q.Set("first", "100")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.Helix+path+"?"+q.Encode(), nil)
	req.Header.Set("Client-Id", c.ID)
	req.Header.Set("Authorization", "Bearer "+tok)
	err = c.do(req, into)
	var he *HTTPError
	if errors.As(err, &he) && he.Status == http.StatusUnauthorized {
		c.mu.Lock()
		c.app = ""
		c.mu.Unlock()
	}
	return err
}

// HTTPError is an answer that is not 2xx.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("twitch answered %d: %s", e.Status, e.Body) }

func (c *Client) do(req *http.Request, into any) error {
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return &HTTPError{Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	if into == nil {
		return nil
	}
	return json.Unmarshal(b, into)
}

func batches(s []string, n int) [][]string {
	var out [][]string
	for len(s) > 0 {
		k := min(n, len(s))
		out = append(out, s[:k])
		s = s[k:]
	}
	return out
}
