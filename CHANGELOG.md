## Unreleased

No changes yet.

## 0.2.0 - 2026-09-04

### Added
- Default missing service config paths to the standard convention

### Fixed
- Send tags as plain strings on volume create; add --quiet/--verbose

## 0.1.0 - 2026-09-04

### Added
- Initialize Go module with cobra CLI skeleton (#1)
- Resolve API URL and output format from flag, env, and file (#2)
- JSON:API client layer (#3)

### Fixed
- Migrate contribution credits to single-role model
- Send a real User-Agent so DriveThruRPG's WAF stops blocking us
