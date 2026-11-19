package roster

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeRoster(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoad(t *testing.T) {
	dir := writeRoster(t, map[string]string{
		"data/epoc001/users.txt":         "# admins\ngithub:galmasi\n\n  github:Alan-Cha  \noidc:gheorghe@us.ibm.com\n",
		"data/epoc001/deleted_users.txt": "github:esnible\n",
	})
	r, err := Load(dir, "epoc001")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"github:galmasi", "github:Alan-Cha", "oidc:gheorghe@us.ibm.com"}; !reflect.DeepEqual(r.Users, want) {
		t.Errorf("Users = %v, want %v", r.Users, want)
	}
	if want := []string{"github:esnible"}; !reflect.DeepEqual(r.Removed, want) {
		t.Errorf("Removed = %v, want %v", r.Removed, want)
	}
}

func TestLoadMissingFileFailsClosed(t *testing.T) {
	dir := writeRoster(t, map[string]string{"data/epoc001/users.txt": "github:galmasi\n"})
	if _, err := Load(dir, "epoc001"); err == nil {
		t.Fatal("missing deleted_users.txt must be an error, never an empty roster")
	}
}

func TestResolvePassThroughAndGrammar(t *testing.T) {
	// Never touch api.github.com from tests: a fake resolver answers logins.
	orig := loginResolver
	defer func() { loginResolver = orig }()
	loginResolver = func(_ context.Context, _ *http.Client, login, _ string) (string, error) {
		if login == "alan-cha" {
			return "14318081", nil
		}
		return "", fmt.Errorf("github account not found (deleted or renamed)")
	}

	in := []string{
		"github:14318081",          // numeric: passes through untouched, no network
		"oidc:gheorghe@us.ibm.com", // oidc: passes through
		"github:alan-cha",          // login: resolved via the (fake) resolver
		"github:gone-user",         // login the resolver cannot resolve
		"github:bad name!",         // malformed
		"oidc:has space@x.com",     // malformed
		"github:",                  // malformed
		"galmasi",                  // no prefix
	}
	out, unresolved := Resolve(context.Background(), in, "")
	wantOut := []string{"github:14318081", "oidc:gheorghe@us.ibm.com", "github:14318081"}
	if !reflect.DeepEqual(out, wantOut) {
		t.Errorf("out = %v, want %v", out, wantOut)
	}
	if len(unresolved) != 5 {
		t.Errorf("unresolved = %v, want 5 entries", unresolved)
	}
}

func TestSubtractRemoveWins(t *testing.T) {
	users := []string{"a", "b", "c"}
	removed := []string{"b", "d"}
	if want := []string{"a", "c"}; !reflect.DeepEqual(Subtract(users, removed), want) {
		t.Errorf("Subtract = %v, want %v", Subtract(users, removed), want)
	}
}
