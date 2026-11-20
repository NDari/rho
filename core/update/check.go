package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CheckInterval is how often Check contacts GitHub.
const CheckInterval = 24 * time.Hour

// Release is a published release.
type Release struct {
	// Version has no "v" prefix.
	Version string
	// Assets maps file names to download URLs.
	Assets map[string]string
}

// Checker looks for new releases.
type Checker struct {
	// Current is the running program.
	Current Current
	// Product is what to update; the zero value means CLI.
	Product Product
	// Home is the Rho home directory, where the last result is kept.
	Home string
	// API is the GitHub API root; empty means https://api.github.com.
	API string
	// Client makes the requests; nil means a client with a timeout.
	Client *http.Client
	// Now returns the time; nil means time.Now.
	Now func() time.Time
}

type state struct {
	Checked time.Time `json:"checked"`
	Latest  string    `json:"latest"`
}

// Cached returns the newer version found by the last check, or "". It does
// not use the network.
func (c *Checker) Cached() string {
	s, _ := c.load()
	if Newer(s.Latest, c.Current.Version) {
		return s.Latest
	}
	return ""
}

// Check returns the latest version when it is newer than the running one,
// or "". It asks GitHub only when the last check is older than
// CheckInterval, and remembers the answer.
func (c *Checker) Check(ctx context.Context) (string, error) {
	s, err := c.load()
	if err == nil && c.now().Sub(s.Checked) < CheckInterval && !s.Checked.After(c.now()) {
		return c.Cached(), nil
	}
	rel, err := c.Latest(ctx)
	if err != nil {
		return "", err
	}
	if Newer(rel.Version, c.Current.Version) {
		return rel.Version, nil
	}
	return "", nil
}

// Latest asks GitHub for the latest release and remembers its version.
// Drafts and pre-releases are not included.
func (c *Checker) Latest(ctx context.Context) (*Release, error) {
	api := c.API
	if api == "" {
		api = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/repos/"+c.product().Repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "rho/"+c.Current.Version)
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("look for updates: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("look for updates: GitHub answered %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("look for updates: %w", err)
	}
	if body.TagName == "" {
		return nil, errors.New("look for updates: the latest release has no tag")
	}
	rel := &Release{Version: strings.TrimPrefix(body.TagName, "v"), Assets: map[string]string{}}
	for _, a := range body.Assets {
		rel.Assets[a.Name] = a.URL
	}
	c.save(state{Checked: c.now(), Latest: rel.Version})
	return rel, nil
}

func (c *Checker) product() Product {
	if c.Product.Repo == "" {
		return CLI
	}
	return c.Product
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Checker) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (c *Checker) load() (state, error) {
	var s state
	data, err := os.ReadFile(filepath.Join(c.Home, c.product().State))
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(data, &s)
	return s, err
}

// save records a check. Failing to save only means checking again sooner.
func (c *Checker) save(s state) {
	data, err := json.Marshal(s)
	if err != nil || c.Home == "" {
		return
	}
	if err := os.MkdirAll(c.Home, 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(c.Home, ".update-*.json")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), filepath.Join(c.Home, c.product().State)) != nil {
		_ = os.Remove(tmp.Name())
	}
}
