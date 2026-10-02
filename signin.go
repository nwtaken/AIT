package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

// Signing an account in. Each AI has its own sign-in command: it opens the
// browser, waits for the user to finish there, and saves the login in the
// account's folder. AIT runs it against that folder and reports progress as
// "signin" events: {id, url} once the sign-in page is known, then
// {id, done, ok, email, err}.

// loginer is implemented by AIs that have a sign-in command.
type loginer interface {
	LoginArgs() []string
}

var loginURL = regexp.MustCompile(`https://[^\s"'\x1b]+`)

var signin struct {
	mu   sync.Mutex
	kill func()
}

// CanSignIn reports whether provider p signs in from AIT's own dialog.
func (a *App) CanSignIn(p string) bool {
	prov := provider(p)
	if prov == nil {
		return false
	}
	_, ok := prov.(loginer)
	return ok && prov.Command() != nil
}

// SignIn starts the sign-in for an account, replacing one already running.
func (a *App) SignIn(acctID string) error {
	acct, ok := a.store.Account(acctID)
	if !ok {
		return fmt.Errorf("no account %q", acctID)
	}
	p := acct.provider()
	lp, ok := p.(loginer)
	cmd := p.Command()
	if !ok || cmd == nil {
		return fmt.Errorf("%s can't be signed in from here", p.Name())
	}
	home := a.store.Home(acct)
	cwd, _ := os.UserHomeDir()
	p.Provision(home, cwd, false)
	env := baseEnv()
	if acct.Dir != "" {
		env = append(env, p.HomeEnv()+"="+acct.Dir)
	}

	a.CancelSignIn()
	c := exec.Command(cmd[0], append(append([]string{}, cmd[1:]...), lp.LoginArgs()...)...)
	c.Env, c.Dir = env, cwd
	pr, pw := io.Pipe()
	c.Stdout, c.Stderr = pw, pw
	hideConsole(c)
	if err := c.Start(); err != nil {
		return fmt.Errorf("could not start %s's sign-in: %w", p.Name(), err)
	}
	kill := killTree(c.Process.Pid)
	signin.mu.Lock()
	signin.kill = kill
	signin.mu.Unlock()

	waited := make(chan error, 1)
	go func() {
		waited <- c.Wait()
		pw.Close()
	}()
	go func() {
		var last string
		sent := false
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			line := strings.TrimSpace(ansiRe.ReplaceAllString(sc.Text(), ""))
			if line == "" {
				continue
			}
			last = line
			if u := loginURL.FindString(line); u != "" && !sent {
				sent = true
				a.emit("signin", map[string]any{"id": acct.ID, "url": u})
			}
		}
		err := <-waited
		kill()
		signin.mu.Lock()
		signin.kill = nil
		signin.mu.Unlock()
		done := map[string]any{"id": acct.ID, "done": true, "ok": a.store.signedIn(acct)}
		if done["ok"] == true {
			a.store.ClearSignedOut(acct.ID)
			done["email"] = maskEmail(a.store.Email(acct))
		} else if err != nil {
			done["err"] = last
		}
		a.emit("signin", done)
	}()
	return nil
}

// CancelSignIn stops a sign-in in progress.
func (a *App) CancelSignIn() {
	signin.mu.Lock()
	kill := signin.kill
	signin.kill = nil
	signin.mu.Unlock()
	if kill != nil {
		kill()
	}
}

// ForgetAccount removes an account that never got signed in (an "Add
// account" the user backed out of). Signed-in accounts are left alone.
func (a *App) ForgetAccount(acctID string) error {
	return a.store.RemoveUnused(acctID)
}
