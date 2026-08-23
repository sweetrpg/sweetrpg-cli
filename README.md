# catalog-cli

[![CI](https://github.com/sweetrpg/catalog-cli/actions/workflows/ci.yaml/badge.svg)](https://github.com/sweetrpg/catalog-cli/actions/workflows/ci.yaml)
[![Coverage](https://img.shields.io/endpoint?url=https://sweetrpg.github.io/catalog-cli/coverage-badge.json)](https://sweetrpg.github.io/catalog-cli/)
[![License](https://img.shields.io/github/license/sweetrpg/catalog-cli.svg)](https://img.shields.io/github/license/sweetrpg/catalog-cli.svg)
[![Issues](https://img.shields.io/github/issues/sweetrpg/catalog-cli.svg)](https://img.shields.io/github/issues/sweetrpg/catalog-cli.svg)
[![PRs](https://img.shields.io/github/issues-pr/sweetrpg/catalog-cli.svg)](https://img.shields.io/github/issues-pr/sweetrpg/catalog-cli.svg)
[![Dependabot](https://badgen.net/github/dependabot/sweetrpg/catalog-cli)](https://badgen.net/github/dependabot/sweetrpg/catalog-cli)

`sweetrpg-catalog` is a command-line client for `catalog-api`: add, edit, view, delete, and link
catalog entities (`volume`, `publisher`, `studio`, `person`, `system`, `license`, `review`,
`contribution`) from a terminal.

## Install

```bash
go install github.com/sweetrpg/catalog-cli/cmd/sweetrpg-catalog@latest
```

## Configuration

The API endpoint resolves in this order:

1. `--api-url` flag
2. `SWEETRPG_CATALOG_API_URL` environment variable
3. `~/.config/sweetrpg/catalog-cli.yaml`

Example config file:

```yaml
api-url: https://catalog-api.dev.sweetrpg.com
assets-web-url: https://assets-web.dev.sweetrpg.com
```

## Authentication

Commands that write require a login. Run once per machine:

```bash
sweetrpg-catalog auth login
```

This opens the Auth0 device-flow login (visit the printed URL and enter the code). Tokens are
stored in your OS keychain under service name `sweetrpg-catalog-cli`; access tokens refresh
automatically. `auth logout` removes them. Auth failures exit with code 3.

## Usage

Entity commands share one shape; `<type>` is one of the entity types above:

```bash
sweetrpg-catalog add <type> <name> [property flags]
sweetrpg-catalog edit <type> <name-or-id> [property flags]
sweetrpg-catalog view <type> <name-or-id> [--json | --yaml]
sweetrpg-catalog delete <type> <name-or-id>
```

Name arguments resolve to record IDs automatically; 24-hex IDs are used as-is. When a name
matches several records an interactive picker lists each candidate's ID, or (with `--yes`)
the command fails and prints the candidates.

Links connect two entities in either argument order:

```bash
sweetrpg-catalog link volume "Dungeon World" publisher "Evil Hat Productions"
sweetrpg-catalog link person "John Wick" volume 507f1f77bcf86cd799439011 --role artist
sweetrpg-catalog unlink volume "Dungeon World" person "John Wick"
```

Linkable pairs: volume-publisher, volume-studio, volume-system, volume-person. Person links to
volumes create or update contribution credits (`--role`, default `author`). Relinking an
existing pair is idempotent.

Volumes also support staged-asset upload for covers:

```bash
sweetrpg-catalog edit volume "Dungeon World" --cover ./dw-cover.png
```

## Scripting

- Pass `--yes` to skip all interactive prompts (delete confirmation included); ambiguous name resolutions then fail instead of prompting.
- Use `view <type> <id> --json` for machine-readable output.
- Exit codes: `0` success, `1` general error, `2` usage error, `3` authentication failure.

## Shell Completion

```bash
source <(sweetrpg-catalog completion bash)   # add to .bashrc
source <(sweetrpg-catalog completion zsh)    # add to .zshrc
sweetrpg-catalog completion fish | source
sweetrpg-catalog completion powershell
```

## Documentation

See [RELEASE.md](RELEASE.md) for how versions get cut and
[CONTRIBUTING.md](CONTRIBUTING.md) for the development workflow.
