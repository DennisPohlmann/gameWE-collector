---
name: security-check
description: Security checklist for every change in the gameWE collector (Go, UDP log parsing, RCON, SQL). Use before finishing, committing or opening a PR for any code, schema, config or packaging change.
---

# Security check for a change (gameWE collector)

Scope: the current diff (`git diff` plus staged and untracked files). Walk
the points below, only as deep as the diff reaches, and report the result.

## Steps

1. Read the diff: `git status --short`, `git diff HEAD`.
2. For each point below that the diff touches, check it in the code (read
   the surrounding function, not just the changed lines).
3. Run `go vet ./...` and `go test ./...`. For changes in `internal/database`,
   `schema/` or `internal/stats` also look at the integration tests that
   cover them.
4. Run the quick secret grep on the diff:
   `git diff HEAD | grep -nEi '(password|passwd|secret|token|apikey|api_key|BEGIN [A-Z ]*PRIVATE KEY)\s*[:=]'`
   Anything that is not `change-me`, an empty value or a test fixture is a
   finding.
5. Fix what you find, then report: touched points, findings with
   file:line, what you fixed, what is left.

## The checklist, adapted to the collector

The numbers follow the original 20-point pre-launch list.

1. **Secrets / keys:** no DB or RCON passwords in code, tests, examples or
   logs. Config prefers `password_file`. RCON passwords come from
   `hlstats_Servers`, never from config committed to git.
2. **Least privilege (RLS equivalent):** the database has no row level
   security; the boundary is the DB user. Collector user writes, web user
   reads with the exclusions in `schema/init/03_grants.sql` (and Postgres).
   New table or column with personal data or secrets: decide the grant in
   both grant files, the grants test must pass.
3. **IDOR:** a log line may only change stats of the server it came from and
   the game of that server. Player lookups go by SteamID + game, never by an
   id taken from the packet.
4. **Git secrets:** see step 4; new local files with credentials go into
   `.gitignore`.
5. **Admin routes:** the collector has no HTTP admin; the equivalent is
   RCON. Commands sent over RCON are fixed formats built by the collector,
   never forwarded text from a log line.
6. **User isolation:** one game server must not be able to write into the
   stats, settings or replies of another (per-server state in `hub.go`,
   replies go to `srv.AddrPort` of the source).
7. **Rate limits:** UDP input is unauthenticated. New per-source or
   per-player state must be bounded (cap maps, drop on full queues), log
   output for rejected packets stays throttled (`udp.go`).
8. **Storage:** files the collector writes (GeoIP DB, logs) live in the
   paths of `packaging/` with the systemd hardening; no world-writable
   files, no paths built from input.
9. **Input validation:** every parser in `internal/logparse` and every new
   event handler: length limits, UTF-8 handling, numeric ranges, SteamID
   formats, unexpected quotes in player names (`"name<1><STEAM_..><CT>"`
   with injected `"` must not fake a second event). Add a test case with
   hostile input for new parsing code.
10. **Unauthenticated entry points:** UDP listener and RCON replies. Only
    known server addresses are accepted; anything new that listens on a
    socket needs the same check.
11. **SQL injection:** placeholders only. `fmt.Sprintf` with SQL is only
    allowed for identifiers from constants (e.g. `store.go` table/column
    choice); check the call sites when touched.
12. **Sensitive logs:** no passwords, RCON passwords, DSNs, full chat
    lines or IP addresses at Info level unless already established; debug
    output stays behind the log level.
13. **Field tampering:** values from logs never set fields they should not
    (e.g. a crafted name must not change skill, hideranking, flags).
14. **Uploads:** not applicable (no uploads). If one appears (e.g. log
    import from a file), limit size and type.
15. **Server logic:** points, awards and ranks are computed in the
    collector only; the plugin's values are input, never trusted totals.
16. **Trim responses:** raw messages and RCON replies contain only what the
    plugin needs; no internal ids, passwords or IPs of other players.
17. **Auth sessions:** RCON auth: passwords per server, connection reset
    on auth failure, no password in error messages.
18. **Dependencies:** a change in `go.mod`/`go.sum` → run `govulncheck ./...`
    (install: `go install golang.org/x/vuln/cmd/govulncheck@v1.9.0`) and
    say what changed.
19. **Record access:** queries that read players or events filter by game
    (and server where relevant); deletes/merges only touch the intended ids.
20. **Attack your own change:** think of one hostile input for the changed
    code (oversized packet, broken UTF-8, `"` and `;` in names, huge
    numbers, unknown server address) and check or test it.
