# Contributing to gameWE - collector

Thank you for your interest in the Go log collector of gameWE. Bug reports, ideas, documentation
fixes and code are all welcome. Please follow the
[Code of Conduct](CODE_OF_CONDUCT.md) in issues, pull requests and anywhere
else in the project.

## Reporting bugs and asking for features

- Search the [existing issues](https://github.com/DennisPohlmann/gameWE-collector/issues)
  first; a comment or a reaction on an existing issue helps more than a duplicate.
- Use the issue forms ("Bug report" or "Feature request") and fill in the
  version and your setup. Logs help a lot.
- Remove passwords, RCON passwords, server tokens, admin API tokens and
  anything else private from logs and configs before you post them.
- **Security problems are not reported in issues**; see [SECURITY.md](SECURITY.md).

gameWE is split into several repositories; please open issues in the one the problem belongs to:

| Repository | Content |
| --- | --- |
| [gameWE-collector](https://github.com/DennisPohlmann/gameWE-collector) | log collector, database schema, `gamewe` commands |
| [gameWE-web](https://github.com/DennisPohlmann/gameWE-web) | website, JSON API, admin area |
| [gameWE-web-content](https://github.com/DennisPohlmann/gameWE-web-content) | optional skins and map pictures |
| [gameWE-plugin-sourcemod](https://github.com/DennisPohlmann/gameWE-plugin-sourcemod) | SourceMod plugin (CS:S, CS:GO, ...) |
| [gameWE-plugin-amxmodx](https://github.com/DennisPohlmann/gameWE-plugin-amxmodx) | AMX Mod X plugin (CS 1.6, CZ, ...) |
| [gameWE-plugin-counterstrikesharp](https://github.com/DennisPohlmann/gameWE-plugin-counterstrikesharp) | CounterStrikeSharp plugin (CS2) |

If you are not sure, open it in [gameWE-collector](https://github.com/DennisPohlmann/gameWE-collector/issues).

## How changes get into `main`

gameWE is maintained by one person. The maintainer merges changes locally and
pushes signed commits straight to `main`; pull requests are not merged through
the GitHub interface.

Pull requests are still very welcome as **proposals**: the maintainer reviews
them, may adjust them, and applies them by hand, keeping you as author or
crediting you with a `Co-authored-by:` line. The pull request is then closed
with a link to the commit. For larger changes, please open an issue first so
we can agree on the approach before you spend time on it.

## Development

Building, testing and the release process are described in
[docs/DEVELOPMENT.md](docs/DEVELOPMENT.md); the code layout and the rules for
changes are in [CLAUDE.md](CLAUDE.md). Before you propose a change, run:

```bash
make test vet
```

Integration tests against MariaDB/MySQL and PostgreSQL are described in [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md); CI runs them on every push.

Please also:

- Keep changes focused: one topic per pull request.
- Update the documentation (`README.md`, `docs/`) in the same change when
  behavior, configuration or commands change.
- Write documentation and code comments in English.
- If a change touches the interface between gameWE components (log lines,
  database tables, the JSON or admin API), describe it in `docs/INTERFACE.md`
  and mention the other repositories that need to follow.

## Commit messages

Releases are made by [semantic-release](https://semantic-release.gitbook.io/)
from [Conventional Commits](https://www.conventionalcommits.org/); nobody tags
by hand. The type of a commit decides the next version:

| Type | Effect |
| --- | --- |
| `fix: ...` | patch release |
| `feat: ...` | minor release |
| `feat!: ...` or a `BREAKING CHANGE:` footer | major release |
| `docs:`, `chore:`, `ci:`, `test:`, `refactor:`, `build:` | no release |

Write the subject in plain English and say in the body why the change is
needed and what a user notices. Commits on `main` are signed.

## License

By contributing, you agree that your contribution is licensed under the
[GNU General Public License v3.0](LICENSE), the license of this repository.
