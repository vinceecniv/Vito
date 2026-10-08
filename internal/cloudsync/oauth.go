package cloudsync

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Connecting is OAuth's authorization-code flow with PKCE, the one meant for
// apps that cannot keep a secret: Vito opens the provider's sign-in page,
// the provider sends the browser back to Vito's own /oauth/callback with a
// code, and Vito trades that code (plus the verifier only it knows) for a
// refresh token. That token is all that is kept.

type pendingAuth struct {
	provider string
	verifier string
	at       time.Time
}

var (
	pendingMu sync.Mutex
	pending   = map[string]pendingAuth{}
)

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// BeginAuth returns the sign-in URL for a provider.
func BeginAuth(p *Provider) (string, error) {
	if p.ClientID == "" {
		return "", fmt.Errorf("%s isn't set up in this version of Vito yet", p.Name)
	}
	state, verifier := randomString(18), randomString(48)
	sum := sha256.Sum256([]byte(verifier))
	pendingMu.Lock()
	for k, v := range pending {
		if time.Since(v.at) > 15*time.Minute {
			delete(pending, k)
		}
	}
	pending[state] = pendingAuth{p.ID, verifier, time.Now()}
	pendingMu.Unlock()
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {p.ClientID},
		"redirect_uri":          {p.Redirect},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	if p.Scope != "" {
		q.Set("scope", p.Scope)
	}
	for k, v := range p.AuthExtra {
		q[k] = v
	}
	return p.AuthURL + "?" + q.Encode(), nil
}

// FinishAuth trades the code from the callback for a refresh token.
func FinishAuth(ctx context.Context, state, code string) (*Provider, string, error) {
	pendingMu.Lock()
	pa, ok := pending[state]
	delete(pending, state)
	pendingMu.Unlock()
	if !ok {
		return nil, "", errors.New("this sign-in has expired or was already used — start it again from Vito")
	}
	p := ProviderByID(pa.provider)
	if p == nil {
		return nil, "", errors.New("unknown provider")
	}
	tok, err := tokenRequest(ctx, p, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {p.Redirect},
		"code_verifier": {pa.verifier},
	})
	if err != nil {
		return nil, "", err
	}
	if tok.Refresh == "" {
		return nil, "", errors.New("the provider gave no refresh token")
	}
	return p, tok.Refresh, nil
}

type tokenResponse struct {
	Access    string `json:"access_token"`
	Refresh   string `json:"refresh_token"`
	ExpiresIn int    `json:"expires_in"`
	Error     string `json:"error"`
	Desc      string `json:"error_description"`
}

func tokenRequest(ctx context.Context, p *Provider, form url.Values) (tokenResponse, error) {
	form.Set("client_id", p.ClientID)
	if p.Scope != "" && form.Get("grant_type") == "refresh_token" {
		form.Set("scope", p.Scope)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return tokenResponse{}, err
	}
	defer resp.Body.Close()
	var t tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return t, fmt.Errorf("token: %s", resp.Status)
	}
	if t.Error != "" || resp.StatusCode != http.StatusOK {
		return t, &AuthError{fmt.Sprintf("%s: %s %s", p.Name, t.Error, t.Desc)}
	}
	return t, nil
}

// AuthError is a refused token: the user revoked Vito's access or the token
// expired, and only connecting again helps.
type AuthError struct{ msg string }

func (e *AuthError) Error() string { return e.msg }

// authTransport puts a fresh access token on every request, refreshing it
// from the refresh token when it is about to expire. A provider that rotates
// refresh tokens (Microsoft) hands back a new one, which saveRefresh keeps.
type authTransport struct {
	p           *Provider
	mu          sync.Mutex
	refresh     string
	saveRefresh func(string)
	access      string
	expires     time.Time
}

func (t *authTransport) token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.access != "" && time.Until(t.expires) > time.Minute {
		return t.access, nil
	}
	tok, err := tokenRequest(ctx, t.p, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.refresh}})
	if err != nil {
		return "", err
	}
	t.access, t.expires = tok.Access, time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second)
	if tok.Refresh != "" && tok.Refresh != t.refresh {
		t.refresh = tok.Refresh
		if t.saveRefresh != nil {
			t.saveRefresh(tok.Refresh)
		}
	}
	return t.access, nil
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := t.token(req.Context())
	if err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+tok)
	return http.DefaultTransport.RoundTrip(r)
}

// Open returns the Remote for a connected provider.
func Open(p *Provider, refresh string, saveRefresh func(string)) Remote {
	hc := &http.Client{Timeout: 2 * time.Minute, Transport: &authTransport{p: p, refresh: refresh, saveRefresh: saveRefresh}}
	return p.open(hc)
}
