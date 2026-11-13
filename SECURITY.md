# Security policy

## Supported versions

Only the latest release of gameWE - collector receives security fixes. Please
update to the [latest release](https://github.com/DennisPohlmann/gameWE-collector/releases/latest)
before you report a problem, and check whether it still happens there.

## Reporting a vulnerability

**Please do not report security vulnerabilities in public issues, pull
requests or discussions.**

Report them privately through GitHub:

1. Open the [Security tab](https://github.com/DennisPohlmann/gameWE-collector/security)
   of this repository.
2. Choose **Report a vulnerability** and fill in the form
   ([direct link](https://github.com/DennisPohlmann/gameWE-collector/security/advisories/new)).

Only the maintainer can read the report. If the form is not available to you,
open an issue that only asks for a private contact channel, without any
details of the problem.

Please include:

- the affected version and how gameWE is installed (package, Docker, Helm);
- what an attacker can do, and what they need for it (for example network
  access to the collector port, a player on a game server, an admin account);
- the steps or a proof of concept to reproduce it;
- any idea for a fix, if you have one.

## What happens next

gameWE is maintained by one person in their spare time, so these are goals,
not guarantees:

- You get an answer within 7 days that the report has arrived.
- The maintainer confirms or rejects the problem and keeps you informed in the
  advisory.
- A confirmed problem is fixed in a new release; the advisory is published
  with the release and credits you, unless you prefer not to be named.

Please give the maintainer reasonable time to release a fix before you publish
details yourself.

## Scope

In scope is the code in this repository, in particular:

- the `gamewe` binary and its commands (`run`, `db`, jobs, `rcon`, `check`)
- parsing of game server log lines received over UDP or HTTP, and the server tokens
- RCON handling, database access and the schema/migrations shipped here
- the container image, the Helm chart and the packaging in this repository

Vulnerabilities in the game servers, in MariaDB/MySQL/PostgreSQL/SQL Server or in Go itself belong to those projects.

Problems that come from a setup against the documentation (for example a
database port open to the internet, or default passwords that were not
changed) are configuration issues rather than vulnerabilities; a public issue
to improve the documentation is welcome for those.
