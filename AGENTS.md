# AGENTS.md

This file provides guidance to Claude Code, Codex, GitHub Copilot, and other AI coding agents
working in this repository.

## About This Project

`sweetrpg-cli` is `sweetrpg`, a Go command-line client for the SweetRPG platform. One
authenticated session (Auth0 device flow, keychain-stored refresh token) works across every
command namespace: `catalog` covers basic CRUD across catalog entity types (volume, publisher,
studio, person, system, license, review, contribution), relationship linking/unlinking, and
cover/sample asset upload against assets-web; `api` is a generic authenticated HTTP passthrough
against any configured service; `entitlements seed` loads an entitlements-api catalog seed (files supplied by the caller) through
its admin API; `game-room import dtrpg` populates a user's own Game Room library
from their DriveThruRPG account. It talks to each service's existing HTTP API only - no backend
changes.

## DTRPG Library Import Matching

`catalog import dtrpg library` (`internal/cmd/import_library.go`, `import_adopt.go`) matches a
product to an existing volume in this order: `dtrpg_product_id` property (already imported,
skip); else exactly one volume with the same normalized title and publisher and no product ID
(adopt: PATCH the product ID and a missing cover, nothing else); else create. Two or more
candidates are skipped and reported. Candidate lookup searches catalog-api with `filter[q]` on
the title's longest word and compares normalized titles in Go; it never pages the whole
catalog. Adoption goes through `PATCH /volumes/:id` with the user's own token and needs an
editor or admin role (catalog-api rejects property changes from submitters). Don't add title,
description, or tag overwrites to adoption.

## Dependencies

Depends on `catalog-objects.go` for API value objects and on cobra (CLI framework),
go-keyring (OS keychain token storage), promptui (interactive prompts). Depended on by nothing -
this is a leaf binary.

## Configuration

Config precedence per service: CLI `--api-url` flag (the targeted service) > `SWEETRPG_<SERVICE>_API_URL` env
var (e.g. `SWEETRPG_CATALOG_API_URL`) > `~/.config/sweetrpg/cli.yaml`'s `services.<service>`
entry.

Auth uses Auth0 device authorization; refresh tokens live in the OS keychain under service name
`sweetrpg-cli`.

## Committing Code

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <description>
```

## Branches and Workflow

* `develop` - integration branch, default branch, target for all PRs.
* `master` - latest released state, nothing committed directly.
* `feature/*`, `fix/*` branched from `develop`; `hotfix/*` branched from `master`.

See `CONTRIBUTING.md` for the full workflow.

## Running Checks Locally

```bash
go build -v ./...
go vet ./...
go test -v ./...
golangci-lint run
```

## Releases

See `RELEASE.md`. Summary: trigger `prepare-release.yaml` (`workflow_dispatch` against
`develop`), which computes the next version from conventional commits via git-cliff and opens
a `release/<version>` PR into `master`. Merging that PR tags the release
(`tag-release.yaml`), which triggers `release.yaml` - re-runs tests, creates a GitHub
Release, and merges `master` back into `develop`.

## Platform Conventions and Decisions

This repo is a submodule of `sweetrpg/platform`. Platform-wide conventions and Architecture
Decision Records live there:

- checked out inside the platform tree: `../../docs/README.md` (convention index) and
  `../../docs/adr/README.md` (platform ADRs, `PADR-*`)
- standalone or module-only checkout:
  <https://github.com/sweetrpg/platform/tree/master/docs> and
  <https://github.com/sweetrpg/platform/tree/master/docs/adr>

Accepted `PADR-*` records are binding constraints. Read the ADR index before proposing a
structural change; if a task needs to contradict an accepted record, stop and say so -
propose a superseding ADR rather than working around it. This repo's own service-local
decisions are `ADR-*` and belong in `docs/adr/` here.

Before implementing any feature, complete the
[feature work checkpoint](https://github.com/sweetrpg/platform/blob/master/docs/openspec.md#feature-work-checkpoint)
and record the answers in the issue or PR.
