# AGENTS.md

This file provides guidance to Claude Code, Codex, GitHub Copilot, and other AI coding agents
working in this repository.

## About This Project

`sweetrpg-cli` is `sweetrpg`, a Go command-line client for the SweetRPG platform. One
authenticated session (Auth0 device flow, keychain-stored refresh token) works across every
command namespace: `catalog` covers basic CRUD across catalog entity types (volume, publisher,
studio, person, system, license, review, contribution), relationship linking/unlinking, and
cover/sample asset upload against assets-web; `api` is a generic authenticated HTTP passthrough
against any configured service; `game-room import dtrpg` populates a user's own Game Room library
from their DriveThruRPG account. It talks to each service's existing HTTP API only - no backend
changes.

## Dependencies

Depends on `catalog-objects.go` for API value objects and on cobra (CLI framework),
go-keyring (OS keychain token storage), promptui (interactive prompts). Depended on by nothing -
this is a leaf binary.

## Configuration

Config precedence per service: CLI flag (catalog only today) > `SWEETRPG_<SERVICE>_API_URL` env
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
