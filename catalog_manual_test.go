//go:build manual

package main

import (
	"os"
	"testing"
	"time"
)

func TestRealClaudeCatalog(t *testing.T) {
	home, _ := os.UserHomeDir()
	c := &claude{userHome: home}
	start := time.Now()
	ms := scanFile(c, c.Command()[0], t.TempDir())
	t.Logf("%d models in %v", len(ms), time.Since(start).Round(time.Millisecond))
	for _, m := range ms {
		t.Logf("  %-28s %-12s %s", m.ID, m.Name, m.Family)
	}
}
