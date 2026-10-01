package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Claude's model versions come from the catalog built into the installed
// Claude Code program, entries like
//
//	{id:"claude-opus-5-5",family:"opus",display_name:"Opus 5.5",knowledge_cutoff:"June 2026",…}
//
// so the switcher lists exactly what that CLI can run, and new versions show
// up when the CLI updates. The scan reads the program once per version (it is
// large) and the result is cached; until then the family aliases stand in.

var claudeFamilies = map[string]string{
	"fable":  "Newest and most capable",
	"opus":   "Deep reasoning",
	"sonnet": "Fast and capable",
	"haiku":  "Fastest and cheapest",
}

// Families that accept a 1M-token context ("<id>[1m]").
var longContext = map[string]bool{"fable": true, "opus": true, "sonnet": true}

var catalogRe = regexp.MustCompile(`\{id:"(claude-[a-z]+-[0-9][0-9-]*)",family:"([a-z]+)",display_name:"([^"]+)"[^{}]{0,200}?knowledge_cutoff:"([^"]+)"`)

type claudeCatalog struct {
	once   sync.Once
	mu     sync.Mutex
	models []Model
}

var catalog claudeCatalog

// load scans (or reads the cache) in the background; Models uses whatever
// is ready.
func (c *claude) loadCatalog(cacheDir string) {
	catalog.once.Do(func() {
		go func() {
			ms := c.scanCatalog(cacheDir)
			catalog.mu.Lock()
			catalog.models = ms
			catalog.mu.Unlock()
		}()
	})
}

func (c *claude) scanCatalog(cacheDir string) []Model {
	cmd := c.Command()
	if len(cmd) == 0 {
		return nil
	}
	return scanFile(c, cmd[0], cacheDir)
}

func scanFile(c *claude, exe, cacheDir string) []Model {
	info, err := os.Stat(exe)
	if err != nil {
		return nil
	}
	key := strconv.FormatInt(info.Size(), 10) + "-" + strconv.FormatInt(info.ModTime().Unix(), 10)
	cache := filepath.Join(cacheDir, "claude-models.json")
	var cached struct {
		Key    string  `json:"key"`
		Models []Model `json:"models"`
	}
	if b, err := os.ReadFile(cache); err == nil && json.Unmarshal(b, &cached) == nil && cached.Key == key && len(cached.Models) > 0 {
		return cached.Models
	}
	f, err := os.Open(exe)
	if err != nil {
		return nil
	}
	defer f.Close()
	seen := map[string]bool{}
	var out []Model
	r := bufio.NewReaderSize(f, 1<<20)
	buf := make([]byte, 0, 9<<20)
	chunk := make([]byte, 8<<20)
	for {
		n, err := io.ReadFull(r, chunk)
		buf = append(buf, chunk[:n]...)
		for _, m := range catalogRe.FindAllSubmatch(buf, -1) {
			id, fam, name := string(m[1]), string(m[2]), string(m[3])
			if seen[id] || claudeFamilies[fam] == "" {
				continue
			}
			seen[id] = true
			out = append(out, Model{ID: id, Name: name, Family: strings.ToUpper(fam[:1]) + fam[1:], Desc: claudeFamilies[fam], Long: longContext[fam]})
		}
		if err != nil {
			break
		}
		// keep a little overlap so an entry split across chunks is still found
		keep := buf[max(0, len(buf)-2048):]
		buf = append(buf[:0], keep...)
	}
	sort.SliceStable(out, func(i, j int) bool { return versionKey(out[i].Name) > versionKey(out[j].Name) })
	if len(out) > 0 {
		b, _ := json.Marshal(map[string]any{"key": key, "models": out})
		os.WriteFile(cache, b, 0o644)
	}
	return out
}

// versionKey turns "Opus 4.10" into a sortable number (4.10 > 4.9).
func versionKey(name string) float64 {
	f := strings.Fields(name)
	if len(f) < 2 {
		return 0
	}
	parts := strings.SplitN(f[len(f)-1], ".", 2)
	major, _ := strconv.Atoi(parts[0])
	minor := 0
	if len(parts) == 2 {
		minor, _ = strconv.Atoi(parts[1])
	}
	return float64(major)*1000 + float64(minor)
}
