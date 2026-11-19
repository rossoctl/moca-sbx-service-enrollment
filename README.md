# moca-sbx-service-enrollment

Who is enrolled in which moca sandbox service, and the companion code that keeps
the deployments in line with this repository.

Each deployment site has a directory under `data/`; a CronJob in that site's
`moca-sbx` namespace clones this repository on a schedule and reconciles the
deployment's enrollment with the site's lists.

## repository structure

* `data/<site>/users.txt` — list, in no particular order, of users to be enrolled.
  Enrollment is contingent on the user ID being valid.
* `data/<site>/deleted_users.txt` — list, in no particular order, of users
  forcibly disenrolled. This overrides `users.txt` on conflict (remove wins).
* `deploy/` — the CronJob manifests (kustomize base + one overlay per site).
* `cmd/enrollment-sync/` — the sync executable.
* `pkg/` — its libraries: `audit` (NDJSON events), `roster` (list parsing and
  github login→id translation), `cp` (control-plane client), `gitclone`
  (repository checkout).
* `Makefile`, `Dockerfile` — build and container image.

User ID structure:

* For w3IDs: `oidc:<w3id>`
* For github IDs: `github:<username>` or `github:<userid>` — a username is
  translated to the numeric id via api.github.com at sync time; a deleted or
  renamed account is reported (the run exits non-zero) and skipped.

## how the sync behaves

One reconciliation run, per site, as a CronJob in that site's `moca-sbx`
namespace (or by hand with `--dry-run`):

* clone this repository, read the site's two lists (any clone or read failure
  aborts the run before anything is touched — fail closed, never reconcile
  against a partial read)
* enroll the users not yet enrolled; unenroll the deleted users who are
* **a user on neither list is never touched**: manually enrolled rosters
  survive the sync
* unenrolling deletes the user's sandboxes first (an admin may delete
  others'), then removes the enrollment; the user's login dies within a token
  TTL and the host janitor frees the workspaces

The sync authenticates as the deployment's synthetic `installer` subject, via
the `SH_INSTALLER_TOKEN` bearer from the deployment's own `moca-mu1` Secret.
It can enroll and unenroll, and nothing else.

Two cases the control plane refuses by design, which the sync logs and steps
over (never works around):

* a subject baked into the deployment's settings (`SH_ALLOWED_SUBJECTS` /
  `SH_ADMIN_SUBJECTS`) cannot be unenrolled (`409 bootstrap_subject`)
* a subject holding a live admin grant cannot be unenrolled
  (`409 admin_granted`) — revoke the role first

Every action and every refusal is an NDJSON event on stdout, so
`kubectl logs job/enrollment-sync-...` is the audit trail. A run that left
anything undone (unresolvable github account, control-plane refusal on a
non-negotiable path, transient error) exits non-zero: the CronJob shows up
failed while the rest of the diff is still applied.

**Known gap:** a removed user's egress credentials are inert (the subject can
no longer log in) but are not erased — `DELETE /v1/credentials/{name}` is
owner-only today. Until moca grows an admin path for that, remove credentials
by hand if the need is immediate.

## building and deploying

```sh
make test          # unit tests
make image         # build and push the container image
make deploy-epoc001  # apply the CronJob to epoc001
make deploy-epoc002  # ... to epoc002
```

No prerequisites: the repository is public (cloned anonymously), and the
installer token is read from the deployment's own `moca-mu1` Secret. Two things
to wire by hand only if circumstances change:

* the repository goes private — create a Secret with a read PAT and wire it to
  `REPO_TOKEN` in `deploy/base/cronjob.yaml`
* the site's API Route certificate is not publicly trusted — add the router CA
  to a Secret, mount it, and set `CP_CA_FILE`

The overlay also applies a NetworkPolicy for the sync's egress (DNS + https)
under the namespace's default-deny.

A dry run from the laptop, against the live deployment, changing nothing:

```sh
make build
make dry-run SITE=epoc001 \
  KUBECONFIG_FILE=~/.kube/config-epoc001 \
  CP_URL=https://sbx-api-moca-sbx.apps.epoc001.ete14.res.ibm.com
```
