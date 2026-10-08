package cloudsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// ErrNotFound is what Get returns for a file that isn't there.
var ErrNotFound = errors.New("not found")

// Remote is the little Vito needs from a cloud drive, inside its app folder:
// paths are slash-separated and relative to that folder.
type Remote interface {
	Get(ctx context.Context, path string) ([]byte, error)
	Put(ctx context.Context, path string, data []byte) error
	// List returns the names of the files directly in dir.
	List(ctx context.Context, dir string) ([]string, error)
	// Account names whose storage this is, for the settings page.
	Account(ctx context.Context) (string, error)
}

// Provider describes one cloud drive: its OAuth endpoints and how to talk to
// it. Vito is a public client (PKCE, no secret), so the id is all it needs.
type Provider struct {
	ID, Name          string
	AuthURL, TokenURL string
	ClientID          string
	Scope             string
	// Redirect is where the provider sends the browser back to. It has to be
	// registered with the provider exactly so.
	Redirect string
	// AuthExtra goes on the authorization URL.
	AuthExtra url.Values
	open      func(hc *http.Client) Remote
}

// The app registrations. A public client id is not a secret; an empty one
// means this build has none yet, and the provider is offered as unavailable.
// VITO_DROPBOX_KEY and VITO_ONEDRIVE_ID override them for testing.
var (
	DropboxAppKey    = ""
	OneDriveClientID = ""
)

// Providers lists the drives Vito can sync through.
func Providers() []*Provider {
	dk, oid := DropboxAppKey, OneDriveClientID
	if v := os.Getenv("VITO_DROPBOX_KEY"); v != "" {
		dk = v
	}
	if v := os.Getenv("VITO_ONEDRIVE_ID"); v != "" {
		oid = v
	}
	return []*Provider{
		{
			ID: "dropbox", Name: "Dropbox",
			AuthURL:  "https://www.dropbox.com/oauth2/authorize",
			TokenURL: "https://api.dropboxapi.com/oauth2/token",
			ClientID: dk,
			// An "App folder" app: Vito sees Apps/Vito and nothing else.
			Redirect:  "http://127.0.0.1:4573/oauth/callback",
			AuthExtra: url.Values{"token_access_type": {"offline"}},
			open:      func(hc *http.Client) Remote { return dropbox{hc} },
		},
		{
			ID: "onedrive", Name: "OneDrive",
			AuthURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
			TokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
			ClientID: oid,
			// Files.ReadWrite.AppFolder: Apps/Vito only, as with Dropbox.
			Scope:    "Files.ReadWrite.AppFolder User.Read offline_access",
			Redirect: "http://localhost:4573/oauth/callback",
			open:     func(hc *http.Client) Remote { return onedrive{hc} },
		},
	}
}

// ProviderByID finds a provider, or nil.
func ProviderByID(id string) *Provider {
	for _, p := range Providers() {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func readErr(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
}

// ---- Dropbox (API v2, app folder) ----

type dropbox struct{ hc *http.Client }

func (d dropbox) call(ctx context.Context, url string, arg any, body []byte, jsonBody bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if jsonBody {
		req.Header.Set("Content-Type", "application/json")
	} else {
		a, _ := json.Marshal(arg)
		req.Header.Set("Dropbox-API-Arg", string(a))
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	return d.hc.Do(req)
}

func (d dropbox) Get(ctx context.Context, path string) ([]byte, error) {
	resp, err := d.call(ctx, "https://content.dropboxapi.com/2/files/download", map[string]string{"path": "/" + path}, nil, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		b, _ := io.ReadAll(resp.Body)
		if strings.Contains(string(b), "not_found") {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("dropbox: %s", b)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, readErr(resp)
	}
	return io.ReadAll(resp.Body)
}

func (d dropbox) Put(ctx context.Context, path string, data []byte) error {
	resp, err := d.call(ctx, "https://content.dropboxapi.com/2/files/upload",
		map[string]any{"path": "/" + path, "mode": "overwrite", "mute": true}, data, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readErr(resp)
	}
	return nil
}

func (d dropbox) List(ctx context.Context, dir string) ([]string, error) {
	body, _ := json.Marshal(map[string]any{"path": "/" + dir})
	url := "https://api.dropboxapi.com/2/files/list_folder"
	var names []string
	for {
		resp, err := d.call(ctx, url, nil, body, true)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusConflict {
			resp.Body.Close()
			return nil, nil // no such folder yet
		}
		if resp.StatusCode != http.StatusOK {
			defer resp.Body.Close()
			return nil, readErr(resp)
		}
		var out struct {
			Entries []struct {
				Tag  string `json:".tag"`
				Name string `json:"name"`
			} `json:"entries"`
			Cursor  string `json:"cursor"`
			HasMore bool   `json:"has_more"`
		}
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, e := range out.Entries {
			if e.Tag == "file" {
				names = append(names, e.Name)
			}
		}
		if !out.HasMore {
			return names, nil
		}
		url = "https://api.dropboxapi.com/2/files/list_folder/continue"
		body, _ = json.Marshal(map[string]string{"cursor": out.Cursor})
	}
}

func (d dropbox) Account(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.dropboxapi.com/2/users/get_current_account", nil)
	if err != nil {
		return "", err
	}
	resp, err := d.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", readErr(resp)
	}
	var a struct {
		Email string `json:"email"`
	}
	err = json.NewDecoder(resp.Body).Decode(&a)
	return a.Email, err
}

// ---- OneDrive (Microsoft Graph, app root) ----

type onedrive struct{ hc *http.Client }

const graph = "https://graph.microsoft.com/v1.0/me/drive/special/approot"

func odPath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

func (o onedrive) Get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, graph+":/"+odPath(path)+":/content", nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, readErr(resp)
	}
	return io.ReadAll(resp.Body)
}

func (o onedrive) Put(ctx context.Context, path string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, graph+":/"+odPath(path)+":/content", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := o.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return readErr(resp)
	}
	return nil
}

func (o onedrive) List(ctx context.Context, dir string) ([]string, error) {
	next := graph + ":/" + odPath(dir) + ":/children?$select=name,file&$top=999"
	var names []string
	for next != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		resp, err := o.hc.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			return nil, nil
		}
		if resp.StatusCode != http.StatusOK {
			defer resp.Body.Close()
			return nil, readErr(resp)
		}
		var out struct {
			Value []struct {
				Name string          `json:"name"`
				File json.RawMessage `json:"file"`
			} `json:"value"`
			Next string `json:"@odata.nextLink"`
		}
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, v := range out.Value {
			if len(v.File) > 0 {
				names = append(names, v.Name)
			}
		}
		next = out.Next
	}
	return names, nil
}

func (o onedrive) Account(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://graph.microsoft.com/v1.0/me?$select=userPrincipalName,mail", nil)
	if err != nil {
		return "", err
	}
	resp, err := o.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", readErr(resp)
	}
	var a struct {
		Mail string `json:"mail"`
		UPN  string `json:"userPrincipalName"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		return "", err
	}
	if a.Mail != "" {
		return a.Mail, nil
	}
	return a.UPN, nil
}
