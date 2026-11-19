// Package roster reads a site's enrollment lists from a checkout of the
// enrollment repository and normalizes them to control-plane subjects.
package roster

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// The member grammar the control plane accepts (packages/control-plane/src/identity.ts,
// MEMBER_SUBJECT_RE): `github:<numeric id>` or `oidc:<provider sub>`. Roster files may
// write a github login instead of the numeric id; the service translates it via
// api.github.com before talking to the control plane.
var (
	githubNumericRe = regexp.MustCompile(`^github:[0-9]+$`)
	githubLoginRe   = regexp.MustCompile(`^github:[A-Za-z0-9-]{1,39}$`)
	oidcSubRe       = regexp.MustCompile(`^oidc:[^/\s]+$`)
)

// Roster is one site's two lists, as written in the repository (github logins
// not yet translated).
type Roster struct {
	Users   []string // desired enrollment (add-only list)
	Removed []string // forced disenrollment; overrides Users on conflict
}

// Load reads data/<site>/users.txt and deleted_users.txt from a checkout of the
// enrollment repository. Blank lines and #-comments are ignored. Either file
// missing is an error: a partial read must never look like an empty roster.
func Load(repoDir, site string) (r Roster, err error) {
	base := filepath.Join(repoDir, "data", site)
	for _, e := range []struct {
		file string
		dst  *[]string
	}{
		{"users.txt", &r.Users},
		{"deleted_users.txt", &r.Removed},
	} {
		f, err := os.Open(filepath.Join(base, e.file))
		if err != nil {
			return Roster{}, fmt.Errorf("open %s/%s: %w", base, e.file, err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			*e.dst = append(*e.dst, line)
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return Roster{}, fmt.Errorf("read %s/%s: %w", base, e.file, err)
		}
	}
	return r, nil
}

// loginResolver is swappable in tests so they never touch api.github.com.
var loginResolver = githubLoginToID

// Resolve translates github logins to numeric ids and validates every entry
// against the member grammar. It returns the normalized list plus the entries it
// could not resolve (deleted or renamed github accounts, malformed lines); the
// caller alerts on those and keeps going with the rest.
//
// Numeric github ids and oidc subjects pass through untouched. Unauthenticated
// api.github.com allows 60 requests/hour per IP; with small rosters and a
// 15-minute schedule that is ample, and ghToken raises it far higher if needed.
func Resolve(ctx context.Context, in []string, ghToken string) (out, unresolved []string) {
	client := &http.Client{Timeout: 15 * time.Second}
	for _, s := range in {
		switch {
		case githubNumericRe.MatchString(s) || oidcSubRe.MatchString(s):
			out = append(out, s)
		case githubLoginRe.MatchString(s):
			login := strings.TrimPrefix(s, "github:")
			id, err := loginResolver(ctx, client, login, ghToken)
			if err != nil {
				unresolved = append(unresolved, s+" ("+err.Error()+")")
				continue
			}
			out = append(out, "github:"+id)
		default:
			unresolved = append(unresolved, s+" (not github:<login|id> or oidc:<sub>)")
		}
	}
	return out, unresolved
}

// githubLoginToID resolves one login via api.github.com. A 404 means the account
// was deleted or renamed; anything else is a transient or configuration error.
func githubLoginToID(ctx context.Context, client *http.Client, login, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/users/"+login, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "moca-sbx-enrollment-sync")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("github account not found (deleted or renamed)")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("api.github.com answered %d", resp.StatusCode)
	}
	var user struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", err
	}
	if user.ID == 0 {
		return "", fmt.Errorf("api.github.com returned no id")
	}
	return fmt.Sprintf("%d", user.ID), nil
}

// Subtract returns the entries of a that are not in b (remove-wins set math).
func Subtract(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, s := range b {
		set[s] = true
	}
	var out []string
	for _, s := range a {
		if !set[s] {
			out = append(out, s)
		}
	}
	return out
}
