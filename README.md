# sweetrpg-cli

[![CI](https://github.com/sweetrpg/sweetrpg-cli/actions/workflows/ci.yaml/badge.svg)](https://github.com/sweetrpg/sweetrpg-cli/actions/workflows/ci.yaml)
[![Coverage](https://img.shields.io/endpoint?url=https://sweetrpg.github.io/sweetrpg-cli/coverage-badge.json)](https://sweetrpg.github.io/sweetrpg-cli/)
[![License](https://img.shields.io/github/license/sweetrpg/sweetrpg-cli.svg)](https://img.shields.io/github/license/sweetrpg/sweetrpg-cli.svg)
[![Issues](https://img.shields.io/github/issues/sweetrpg/sweetrpg-cli.svg)](https://img.shields.io/github/issues/sweetrpg/sweetrpg-cli.svg)
[![PRs](https://img.shields.io/github/issues-pr/sweetrpg/sweetrpg-cli.svg)](https://img.shields.io/github/issues-pr/sweetrpg/sweetrpg-cli.svg)
[![Dependabot](https://badgen.net/github/dependabot/sweetrpg/sweetrpg-cli)](https://badgen.net/github/dependabot/sweetrpg/sweetrpg-cli)

`sweetrpg` is a command-line client for the SweetRPG platform: one authenticated session usable
against every service. `sweetrpg catalog` covers add/edit/view/delete/link for catalog entities
(`volume`, `publisher`, `studio`, `person`, `system`, `license`, `review`, `contribution`);
`sweetrpg api` is a generic authenticated request passthrough for any configured service.

## Install

```bash
go install github.com/sweetrpg/sweetrpg-cli/cmd/sweetrpg@latest
```

## Configuration

Each service's base URL resolves in this order:

1. `--api-url` flag (catalog only today) - a full URL
2. `SWEETRPG_<SERVICE>_API_URL` environment variable (e.g. `SWEETRPG_CATALOG_API_URL`,
   `SWEETRPG_GAME_ROOM_API_URL`) - also a full URL
3. `~/.config/sweetrpg/cli.yaml`'s `baseURL` plus a `services.<service>` path

Example config file. Every service's base URL is `baseURL` joined with its own path under
`services` - including `assetsWeb`, which isn't itself a platform API but is a network path like
the rest:

```yaml
baseURL: https://dev.sweetrpg.com
services:
  catalog: /api/0/catalog
  gameRoom: /api/0/game-room
  assetsWeb: /assets
```

A service that lives on a different host entirely (a local port during dev, say) skips `baseURL`
for that one service via its env var, which always takes a full URL and overrides the config
file's path-under-`baseURL` resolution.

## Authentication

Commands that write require a login. A release build ships with its Auth0 tenant baked in, but
that's a default, not a hardcode - it resolves in this order:

1. `SWEETRPG_AUTH_DOMAIN` / `SWEETRPG_AUTH_CLIENT_ID` / `SWEETRPG_AUTH_AUDIENCE` environment
   variables
2. `~/.config/sweetrpg/cli.yaml`'s `authTenant` section
3. The values baked in via `-ldflags` at release time

For dev runs against plain `go run` (nothing baked in), set one of the first two:

```bash
export SWEETRPG_AUTH_DOMAIN=dev-xxxx.us.auth0.com
export SWEETRPG_AUTH_CLIENT_ID=...
export SWEETRPG_AUTH_AUDIENCE=https://catalog-api
```

or in the config file:

```yaml
authTenant:
  domain: dev-xxxx.us.auth0.com
  clientId: ...
  audience: https://catalog-api
```

Run once per machine:

```bash
sweetrpg auth login
```

This opens the Auth0 device-flow login (visit the printed URL and enter the code). The session is
shared across every command namespace (`catalog`, `api`, `game-room`, ...) - one login covers all
of them. Tokens are stored in your OS keychain under service name `sweetrpg-cli`; access tokens
refresh automatically. `auth logout` removes them. Auth failures exit with code 3.

Reads don't require a login: `catalog view` (and name resolution it performs) hits public
endpoints and works with no stored session. Writes (`add`, `edit`, `delete`, `link`, `unlink`)
require one.

## Catalog commands

Entity commands share one shape; `<type>` is one of the entity types above:

```bash
sweetrpg catalog add <type> <name> [property flags]
sweetrpg catalog edit <type> <name-or-id> [property flags]
sweetrpg catalog view <type> <name-or-id> [--json | --yaml]
sweetrpg catalog delete <type> <name-or-id>
```

Name arguments match case-insensitively and partially (exact matches win when both kinds
hit); 24-hex IDs are used as-is. When a name matches several records an interactive picker
lists each candidate's ID, or (with `--yes`) the command fails and prints the candidates.

`catalog view volume` prints a viewable `coverURL` alongside a volume's own fields when it has a
cover and `assets-web-url` is configured; `--json`/`--yaml` stay the server's raw representation.

To see what a fuzzy query will hit before resolving, use `search`:

```bash
sweetrpg catalog search <type> <query>    # prints "ID<TAB>name" per hit
```

Links connect two entities in either argument order:

```bash
sweetrpg catalog link volume "Dungeon World" publisher "Evil Hat Productions"
sweetrpg catalog link person "John Wick" volume 507f1f77bcf86cd799439011 --role artist
sweetrpg catalog unlink volume "Dungeon World" person "John Wick"
```

Linkable pairs: volume-publisher, volume-studio, volume-system, volume-person. Person links to
volumes create or update contribution credits (`--role`, default `author`). Relinking an
existing pair is idempotent.

Volumes also support staged-asset upload for covers:

```bash
sweetrpg catalog edit volume "Dungeon World" --cover ./dw-cover.png
```

`--cover` accepts png, jpeg, or webp files and can be combined with property
flags. Uploads require a session and an assets-web base URL (`--assets-web-url` flag,
`SWEETRPG_ASSETS_WEB_URL` env var, or `services.assetsWeb` in the config file); they talk to
assets-web directly, so a `--curl` run previews the linking PATCH but not the upload itself.

## DriveThruRPG login

`sweetrpg dtrpg login`/`sweetrpg dtrpg logout` manage one DriveThruRPG application key, shared by
every command that imports from your DriveThruRPG library (`catalog import dtrpg library`,
`game-room import dtrpg`). It's one external account either way, so there's one login:

```bash
sweetrpg dtrpg login                 # paste a key from your DTRPG account settings
sweetrpg dtrpg login --credentials   # or enter email + password to mint one
```

The key is kept in the OS keychain under service `sweetrpg-cli`, account
`dtrpg-app-key` - separate from the platform session. It is exchanged for a short-lived session
on every run; the session token is never written to disk. Passwords are read at a masked prompt
and discarded after the exchange. `sweetrpg dtrpg logout` deletes the stored key.

## Importing a DriveThruRPG library into the catalog

`catalog import dtrpg library` bulk-loads the volumes in your DriveThruRPG library into the
catalog. It drives the same `POST /volumes` and `POST /publishers` endpoints as `catalog add`, so
imported records land as submitted versions for normal review.

Run the import (requires both a platform login and `sweetrpg dtrpg login`):

```bash
sweetrpg catalog import dtrpg library --dry-run     # show the plan, write nothing
sweetrpg catalog import dtrpg library               # create volumes and publishers
```

Each product maps to a volume: title, short description, and category filters as tags. The
DriveThruRPG product ID and ISBN (when present) are stored as `dtrpg_*` properties - purchase
date and order ID are not, since they're personal-order facts rather than catalog data. The
product's cover image is downloaded and stored as the volume's own cover asset, not referenced
by URL. Publisher names resolve case-insensitively to existing publisher records, creating one
on a miss. Re-runs are idempotent - a product whose `dtrpg_product_id` already appears on a
volume is skipped.

`catalog import dtrpg library` is meant to be run by an admin or editor: created volumes,
publishers, and cover links land as **live records**, not review-queue submissions - a bulk
import can create hundreds or thousands of records, and routing all of that through review would
make the queue unusable. There's no separate "publish immediately" flag; it follows from the
caller's role the same way `POST /publishers` and `PATCH /volumes` already do. A submitter-role
token still works for the writes that support it, but expect it to behave differently than
documented here.

Flags:

- `--dry-run` - fetch the library and print the plan (to import / already imported / skipped)
  without any write.
- `--include-archived` - also import products whose DriveThruRPG files are archived (skipped by
  default).
- `--page-size` - DriveThruRPG retrieval page size; `0` uses the server default.

A per-product failure is isolated: the run continues, the failure is listed in the summary, and
the command exits `1`. Missing platform session exits `3`; missing DriveThruRPG key exits `1`
with a pointer to `sweetrpg dtrpg login`.

## Populating your Game Room library from DriveThruRPG

`game-room import dtrpg` matches your DriveThruRPG library against volumes already in the
SweetRPG catalog and adds every match to your own Game Room library. It never creates a catalog
record - a product with no matching catalog volume is skipped and reported, not imported. Use
`catalog import dtrpg library` (an admin/editor tool, see above) to populate the shared catalog
itself first. Uses the same DriveThruRPG login as the catalog import - run `sweetrpg dtrpg login`
once and both commands can use it.

Run the match-and-add:

```bash
sweetrpg game-room import dtrpg --dry-run     # show what would be added, write nothing
sweetrpg game-room import dtrpg               # add every matched volume to your library
```

Matching is by the `dtrpg_product_id` property the catalog import records on each volume. The
completion summary reports counts of products added, already in your library, and skipped
because no catalog volume matches yet (with their titles), so you understand why your full
DriveThruRPG library may not fully populate your Game Room library. `game-room import dtrpg
logout` deletes the stored key.

## `sweetrpg api`: generic authenticated requests

For endpoints the typed `catalog` commands don't cover, `api` sends an authenticated request
against any configured service, in the spirit of `gh api`:

```bash
sweetrpg api GET /volumes/123 --service catalog
sweetrpg api POST /publishers --service catalog --field name="Evil Hat Productions"
sweetrpg api GET /users/me --service users -H "X-Request-Id: abc123"
```

`--field key=value` type-sniffs the value (`true`/`false`/numeric encode as their JSON type,
everything else as a string); `--raw-field key=value` always encodes a string. The method
defaults to `GET` with no body, `POST` when `--field`/`--raw-field` is present. Combine with
`--curl` to preview the request instead of sending it.

## Scripting

- Pass `--yes` to skip all interactive prompts; ambiguous name resolutions then fail instead of prompting. Deletes additionally require `--force` when stdin is not a TTY - `--yes` alone never deletes in a script.
- Use `catalog view <type> <id> --json` for machine-readable output.
- Pass `--curl` to print the equivalent cURL command(s) instead of calling the API. Nothing is sent; the bearer token is printed as `<redacted>`. Flows that need server data to continue (name resolution feeding later requests) stop after their first request, so pass IDs instead of names to see write requests directly.
- Exit codes: `0` success, `1` general error, `2` usage error, `3` authentication failure.

## Shell Completion

```bash
source <(sweetrpg completion bash)   # add to .bashrc
source <(sweetrpg completion zsh)    # add to .zshrc
sweetrpg completion fish | source
sweetrpg completion powershell
```

## Documentation

See [RELEASE.md](RELEASE.md) for how versions get cut and
[CONTRIBUTING.md](CONTRIBUTING.md) for the development workflow.
