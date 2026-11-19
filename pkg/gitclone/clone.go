// Package gitclone shallow-clones the enrollment repository, with the access
// token kept out of argv and logs.
package gitclone

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Clone shallow-clones the enrollment repo into a fresh temp directory and
// returns its path; the caller removes it. A token (github PAT or similar) is
// passed via the URL's userinfo so it never appears in argv or the audit log.
//
// Fail closed: any clone failure is an error, and the caller aborts the run
// without touching the control plane — a partial read must never look like an
// empty roster.
func Clone(ctx context.Context, repoURL, token string) (string, error) {
	dir, err := os.MkdirTemp("", "enrollment-repo-")
	if err != nil {
		return "", fmt.Errorf("temp dir: %w", err)
	}
	if token != "" {
		u, err := url.Parse(repoURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			os.RemoveAll(dir)
			return "", fmt.Errorf("a token requires an https repo URL, got %s", RedactURL(repoURL))
		}
		// github-style token as username, x-oauth-basic as the conventional dummy password
		u.User = url.UserPassword(token, "x-oauth-basic")
		repoURL = u.String()
	}
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--quiet", repoURL, filepath.Join(dir, "repo"))
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME="+dir,
	)
	// The source is chosen by configuration (this repository, over authenticated
	// https or a local path), so git's dubious-ownership guard has nothing to add.
	// The guard only honors the global config file — not -c or environment — so write
	// one in the private HOME above; this also lets a laptop dry-run clone a checkout
	// owned by another uid (a container volume mount).
	if err := os.WriteFile(filepath.Join(dir, ".gitconfig"),
		[]byte("[safe]\n\tdirectory = *\n"), 0o600); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("write git config: %w", err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("git clone: %v: %s", err, redactOutput(string(out)))
	}
	return filepath.Join(dir, "repo"), nil
}

// RedactURL keeps a token embedded in a URL out of error messages.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User("***")
	return u.String()
}

// redactOutput scrubs a token from git's error output, just in case it echoes the URL.
func redactOutput(s string) string {
	if at := strings.Index(s, "://"); at >= 0 {
		rest := s[at+3:]
		// only scrub the userinfo form token@host
		if slash := strings.IndexAny(rest, "/ \n"); slash > 0 && strings.Contains(rest[:slash], "@") {
			return s[:at+3] + "***" + rest[slash:]
		}
	}
	return s
}
