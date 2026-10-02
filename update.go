package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Updates.
//
// AIT's only network access of its own: it asks GitHub for the latest
// release of UpdateRepo. Nothing is downloaded until the user clicks
// Update, and then only:
//   - the installer asset, from this repository's release downloads, over HTTPS;
//   - its published SHA-256, which the download must match exactly.
// The verified installer then runs (silently, keeping the user's data) and
// restarts AIT. Users can turn the automatic check off in Settings.

// Version is set at build time: -ldflags "-X main.Version=1.2.3".
var Version = "1.0.0"

// UpdateRepo is the GitHub repository releases are published to.
const UpdateRepo = "nwtaken/AIT"

// repoID is GitHub's permanent number for UpdateRepo. Asking by number keeps
// updates working when the account or repository is renamed; downloads must
// still come from that same repository's releases.
const repoID = "1400644785"

// The only addresses the updater talks to. Variables so tests can point
// them at a local fake; nothing else ever changes them.
var (
	apiBase      = "https://api.github.com"
	downloadBase = "https://github.com"
)

const (
	installerAsset = "AIT-setup.exe"
	checksumAsset  = "AIT-setup.exe.sha256"
	checkEvery     = 6 * time.Hour
)

type UpdateInfo struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	Notes     string `json:"notes"`
	Size      int64  `json:"size"`
	Ready     bool   `json:"ready"` // downloaded and verified; installs on close

	installerURL, checksumURL string
	repo                      string // owner/name the release lives in now
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// startUpdateChecks checks shortly after start and then every few hours,
// telling the page when there is something newer.
func (a *App) startUpdateChecks() {
	go func() {
		time.Sleep(15 * time.Second)
		for {
			if c := a.store.Config(); c.autoUpdate() {
				if u, err := a.CheckUpdate(); err == nil && u.Available {
					if c.AutoInstall {
						if _, err := a.prepareUpdate(); err == nil {
							u.Ready = true // installs when AIT closes
						}
					}
					a.emit("update:available", u)
				}
			}
			time.Sleep(checkEvery)
		}
	}()
}

// CheckUpdate asks GitHub for the latest release.
func (a *App) CheckUpdate() (UpdateInfo, error) {
	info := UpdateInfo{Current: Version}
	req, _ := http.NewRequest("GET", apiBase+"/repositories/"+repoID+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "AIT/"+Version)
	resp, err := httpClient.Do(req)
	if err != nil {
		return info, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return info, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Body   string `json:"body"`
		Draft  bool   `json:"draft"`
		Page   string `json:"html_url"` // <downloadBase>/<owner>/<name>/releases/tag/<tag>
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return info, err
	}
	info.Latest = strings.TrimPrefix(rel.Tag, "v")
	info.Notes = rel.Body
	if rest, ok := strings.CutPrefix(rel.Page, downloadBase+"/"); ok {
		if i := strings.Index(rest, "/releases/"); i > 0 {
			info.repo = rest[:i]
		}
	}
	for _, as := range rel.Assets {
		switch as.Name {
		case installerAsset:
			info.installerURL, info.Size = as.URL, as.Size
		case checksumAsset:
			info.checksumURL = as.URL
		}
	}
	info.Available = !rel.Draft && newer(info.Latest, Version) && info.installerURL != "" && info.checksumURL != ""
	return info, nil
}

// InstallUpdate downloads, verifies and runs the latest installer, then
// quits so it can replace AIT. Only ever started by the user's click.
func (a *App) InstallUpdate() error {
	path, err := a.prepareUpdate()
	if err != nil {
		return err
	}
	// /S: silent, the user already said yes. /RELAUNCH: start AIT again when done.
	if err := exec.Command(path, "/S", "/RELAUNCH").Start(); err != nil {
		return err
	}
	a.allowQuit.Store(true)
	runtime.Quit(a.ctx)
	return nil
}

// prepareUpdate downloads the latest installer and verifies it against its
// published checksum. It returns the path of a verified installer, reusing
// one prepared earlier.
func (a *App) prepareUpdate() (string, error) {
	a.updMu.Lock()
	defer a.updMu.Unlock()
	u, err := a.CheckUpdate()
	if err != nil {
		return "", err
	}
	if !u.Available {
		return "", errors.New("AIT is already up to date")
	}
	if a.prepared != "" && a.preparedVer == u.Latest && fileExists(a.prepared) {
		return a.prepared, nil
	}
	prefix := downloadBase + "/" + u.repo + "/releases/download/"
	if u.repo == "" || !strings.HasPrefix(u.installerURL, prefix) || !strings.HasPrefix(u.checksumURL, prefix) {
		return "", errors.New("the update is not from AIT's own releases; not installing it")
	}
	sum, err := fetch(u.checksumURL, 1<<10)
	if err != nil {
		return "", fmt.Errorf("could not get the checksum: %w", err)
	}
	want := strings.ToLower(strings.Fields(string(sum) + " ")[0])
	if len(want) != 64 {
		return "", errors.New("the published checksum is malformed")
	}
	data, err := fetch(u.installerURL, 200<<20)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return "", errors.New("the download does not match its checksum; not installing it")
	}
	dir := filepath.Join(os.TempDir(), "AIT-update")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "AIT-setup-"+u.Latest+".exe")
	if err := os.WriteFile(path, data, 0o755); err != nil {
		return "", err
	}
	a.prepared, a.preparedVer = path, u.Latest
	return path, nil
}

// installOnExit runs a prepared update as AIT closes (Settings: install
// updates automatically). No relaunch: the user was closing AIT.
func (a *App) installOnExit() {
	a.updMu.Lock()
	path := a.prepared
	a.updMu.Unlock()
	if path != "" && a.store.Config().AutoInstall && fileExists(path) {
		exec.Command(path, "/S").Start()
	}
}

// WhatsNew reports, once, that AIT was updated since it last ran, with that
// release's notes from GitHub.
func (a *App) WhatsNew() UpdateInfo {
	c := a.store.Config()
	last := c.LastVersion
	if last == Version {
		return UpdateInfo{}
	}
	c.LastVersion = Version
	a.store.saveConfig(c)
	if last == "" || !newer(Version, last) {
		return UpdateInfo{} // first run, or a downgrade: nothing to announce
	}
	info := UpdateInfo{Current: Version, Latest: Version, Available: true}
	req, _ := http.NewRequest("GET", apiBase+"/repositories/"+repoID+"/releases/tags/v"+Version, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "AIT/"+Version)
	if resp, err := httpClient.Do(req); err == nil {
		defer resp.Body.Close()
		var rel struct {
			Body string `json:"body"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel) == nil {
			info.Notes = rel.Body
		}
	}
	return info
}

func (a *App) AppVersion() string { return Version }

func fetch(url string, limit int64) ([]byte, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "AIT/"+Version)
	c := &http.Client{Timeout: 10 * time.Minute}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// newer reports whether version a is later than b ("1.2.10" > "1.2.9").
func newer(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}
