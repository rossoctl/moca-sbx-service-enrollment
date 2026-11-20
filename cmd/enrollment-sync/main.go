// The enrollment sync: reconcile a moca sandbox deployment's enrollment with the
// rosters in the enrollment repository. Run as a CronJob in the deployment's
// moca-sbx namespace (deploy/), or by hand with --dry-run.
//
// The contract (one reconciliation run):
//
//   - clone the repo, read data/<site>/users.txt (enroll) and deleted_users.txt (force
//     disenroll; overrides the other list on conflict)
//   - translate github logins to numeric ids via api.github.com
//   - read the deployment's live subjects (GET /v1/subjects, as the installer subject)
//   - enroll the desired who are not enrolled; unenroll the removed who are
//   - a subject on NEITHER list is never touched, so manual rosters survive
//   - a settings-baked subject (SH_ALLOWED_SUBJECTS/SH_ADMIN_SUBJECTS) or one holding a
//     live admin grant is refused by the control plane: log the refusal and continue
//   - unenroll first deletes the subject's sandboxes (an admin may delete others')
//
// Anything unexpected — clone failure, control-plane failure, an unresolvable github
// login — is logged as an NDJSON audit event; the run keeps going where that is safe
// (a single bad roster entry) and aborts where it is not (repo or control plane
// unreachable), always exiting non-zero when anything was left undone.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/rossoctl/moca-sbx-service-enrollment/pkg/audit"
	"github.com/rossoctl/moca-sbx-service-enrollment/pkg/cp"
	"github.com/rossoctl/moca-sbx-service-enrollment/pkg/gitclone"
	"github.com/rossoctl/moca-sbx-service-enrollment/pkg/roster"
)

func main() {
	var (
		site    = envOr("SITE", "")
		repoURL = envOr("ENROLLMENT_REPO_URL", "")
		cpURL   = envOr("CONTROL_PLANE_URL", "")
		token   = envOr("SH_INSTALLER_TOKEN", "")
		caFile  = envOr("CP_CA_FILE", "")
		ghToken = envOr("GITHUB_TOKEN", "")
		repoTok = envOr("REPO_TOKEN", "")
	)
	dryRun := flag.Bool("dry-run", os.Getenv("DRY_RUN") == "true", "compute and audit the diff, change nothing")
	timeout := flag.Duration("timeout", 10*time.Minute, "whole-run timeout")
	flag.Parse()

	log := audit.Logger{}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	failed := false
	fail := func(kind string, fields map[string]any, err error) {
		fields["error"] = err.Error()
		log.Event(kind, fields)
		failed = true
	}

	log.Event("run_start", map[string]any{
		"site": site, "dry_run": *dryRun, "control_plane": cpURL,
	})

	for k, v := range map[string]string{
		"SITE": site, "ENROLLMENT_REPO_URL": repoURL, "CONTROL_PLANE_URL": cpURL, "SH_INSTALLER_TOKEN": token,
	} {
		if v == "" {
			fail("config_error", map[string]any{"missing": k}, fmt.Errorf("required setting is empty"))
			os.Exit(1)
		}
	}

	// 1. The roster: clone, read, translate. Fail closed — never reconcile against a
	// partial or missing read.
	repoDir, err := gitclone.Clone(ctx, repoURL, repoTok)
	if err != nil {
		fail("clone_error", map[string]any{"repo": gitclone.RedactURL(repoURL)}, err)
		os.Exit(1)
	}
	defer os.RemoveAll(repoDir)

	r, err := roster.Load(repoDir, site)
	if err != nil {
		fail("roster_error", map[string]any{"site": site}, err)
		os.Exit(1)
	}
	users, unresolvedUsers := roster.Resolve(ctx, r.Users, ghToken)
	removed, unresolvedRemoved := roster.Resolve(ctx, r.Removed, ghToken)
	for _, list := range []struct {
		name    string
		entries []string
	}{
		{"users.txt", unresolvedUsers}, {"deleted_users.txt", unresolvedRemoved},
	} {
		for _, e := range list.entries {
			fail("roster_entry_error", map[string]any{"site": site, "list": list.name, "entry": e}, fmt.Errorf("unresolvable roster entry"))
		}
	}

	// Remove wins on conflict.
	desired := roster.Subtract(users, removed)
	toRemove := removed

	log.Event("roster", map[string]any{
		"site": site, "desired": len(desired), "removed": len(toRemove),
		"unresolved": len(unresolvedUsers) + len(unresolvedRemoved),
	})

	// 2. Live state.
	client, err := cp.New(cpURL, token, caFile, "moca-sbx-enrollment-sync")
	if err != nil {
		fail("config_error", nil, err)
		os.Exit(1)
	}
	live, err := client.ListSubjects(ctx)
	if err != nil {
		fail("control_plane_error", map[string]any{"op": "list_subjects"}, err)
		os.Exit(1)
	}
	enrolled := make(map[string]cp.Subject, len(live))
	for _, s := range live {
		enrolled[s.Subject] = s
	}

	// 3. The diff. A subject on neither list is never touched.
	var toEnroll []string
	for _, s := range desired {
		if e, ok := enrolled[s]; !ok || !e.Enrolled {
			toEnroll = append(toEnroll, s)
		}
	}
	var unenroll []string
	for _, s := range toRemove {
		if e, ok := enrolled[s]; ok && e.Enrolled {
			unenroll = append(unenroll, s)
		}
	}

	log.Event("diff", map[string]any{
		"enroll": toEnroll, "unenroll": unenroll, "enrolled_seen": len(enrolled),
	})

	if *dryRun {
		log.Event("run_end", map[string]any{"dry_run": true, "would_enroll": len(toEnroll), "would_unenroll": len(unenroll), "failed": failed})
		exitOn(failed)
	}

	// 4. Removals first, so a user moving between lists ends up unenrolled-then-clean.
	for _, s := range unenroll {
		if err := removeSubject(ctx, client, s, log); err != nil {
			fail("unenroll_error", map[string]any{"subject": s}, err)
		}
	}

	// 5. Enrollments.
	for _, s := range toEnroll {
		if err := client.Enroll(ctx, s); err != nil {
			if cp.IsRefusal(err) {
				log.Event("enroll_refused", map[string]any{"subject": s, "error": err.Error()})
				continue
			}
			fail("enroll_error", map[string]any{"subject": s}, err)
		} else {
			log.Event("enrolled", map[string]any{"subject": s})
		}
	}

	log.Event("run_end", map[string]any{
		"enrolled": len(toEnroll), "unenrolled": len(unenroll), "failed": failed,
	})
	exitOn(failed)
}

// removeSubject deletes the subject's sandboxes, then unenrolls it. The control
// plane refuses a settings-baked subject or a live admin grant; those come back
// as refusals and are logged without failing the run.
func removeSubject(ctx context.Context, client *cp.Client, s string, log audit.Logger) error {
	sandboxes, err := client.ListSandboxes(ctx)
	if err != nil {
		return fmt.Errorf("list sandboxes: %w", err)
	}
	for _, sb := range sandboxes {
		if sb.Owner != s {
			continue
		}
		if err := client.DeleteSandbox(ctx, sb.ID); err != nil {
			if cp.IsRefusal(err) {
				log.Event("delete_sandbox_refused", map[string]any{"subject": s, "sandbox": sb.ID, "error": err.Error()})
				continue
			}
			return fmt.Errorf("delete sandbox %s: %w", sb.ID, err)
		}
		log.Event("sandbox_deleted", map[string]any{"subject": s, "sandbox": sb.ID, "state": sb.State})
	}
	if err := client.Unenroll(ctx, s); err != nil {
		if cp.IsRefusal(err) {
			log.Event("unenroll_refused", map[string]any{"subject": s, "error": err.Error()})
			return nil
		}
		return err
	}
	log.Event("unenrolled", map[string]any{"subject": s})
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func exitOn(failed bool) {
	if failed {
		os.Exit(1)
	}
	os.Exit(0)
}
