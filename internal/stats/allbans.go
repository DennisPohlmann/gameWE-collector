// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"context"
	"slices"
	"strings"
	"time"
)

// hlstats_Bans holds the bans of every known Steam account, one row per
// account and source: "steam" (the Steam Web API's GetPlayerBans; a row for
// every account asked, banned or not, so last_checked tells when to ask
// again) and "sourcebans" (a running ban of the public SourceBans list;
// expired and lifted bans are deleted). hlstats_Bans_Players says which
// players of which ranking a banned account is. The collector's bans-sync
// job (internal/collector/bansync.go) writes both; the web only reads them.
const (
	BanSourceSteam      = "steam"
	BanSourceSourceBans = "sourcebans"
)

// BanAccount is one Steam ID of a player (hlstats_PlayerUniqueIds).
type BanAccount struct {
	PlayerID int64
	Game     string
	UniqueID string
}

// SteamBan is the answer of the Steam Web API for one account.
type SteamBan struct {
	SteamID64        uint64
	VACBanned        bool
	VACBans          int
	GameBans         int
	CommunityBanned  bool
	EconomyBan       string // "none", "probation", "banned"
	DaysSinceLastBan int
}

// Banned reports whether the row counts as a ban on the website: a VAC or
// game ban (Steam marks both on the profile).
func (b SteamBan) Banned() bool { return b.VACBanned || b.VACBans > 0 || b.GameBans > 0 }

// SourceBan is a running ban of the SourceBans list for one account (the
// newest one when the list shows several).
type SourceBan struct {
	SteamID64     uint64
	Reason        string
	Admin         string
	LengthMinutes int       // 0: permanent or unknown
	Created       time.Time // zero: unknown
	Ends          time.Time // zero: permanent or unknown
}

// BanPlayer is a player of a banned account (hlstats_Bans_Players).
type BanPlayer struct {
	PlayerID int64
	Game     string
}

// BanAccounts returns the Steam IDs of all players, active and inactive, of
// every ranking (bots left out); the caller converts them to SteamID64.
func (s *Store) BanAccounts(ctx context.Context) ([]BanAccount, error) {
	rows, err := s.query(ctx, `SELECT playerId, game, uniqueId FROM hlstats_PlayerUniqueIds WHERE uniqueId NOT LIKE 'BOT%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BanAccount
	for rows.Next() {
		var a BanAccount
		if err := rows.Scan(&a.PlayerID, &a.Game, &a.UniqueID); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SteamChecked returns when each account was last asked at Steam (Unix
// time), by SteamID64.
func (s *Store) SteamChecked(ctx context.Context) (map[uint64]int64, error) {
	rows, err := s.query(ctx, `SELECT steamid64, last_checked FROM hlstats_Bans WHERE source = ?`, BanSourceSteam)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uint64]int64{}
	for rows.Next() {
		var id uint64
		var at int64
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = at
	}
	return out, rows.Err()
}

// changedAt is the updated_at of an upsert: the new value when one of cols
// changed, else the stored one. On MariaDB it must come first in the
// update list (later assignments see the new values).
func (s *Store) changedAt(cols ...string) string {
	old, new := func(c string) string { return c }, func(c string) string { return "VALUES(" + c + ")" }
	if s.db.Postgres() {
		old, new = func(c string) string { return "hlstats_Bans." + c }, func(c string) string { return "EXCLUDED." + c }
	}
	diff := make([]string, len(cols))
	for i, c := range cols {
		diff[i] = old(c) + " IS DISTINCT FROM " + new(c)
		if !s.db.Postgres() {
			diff[i] = "NOT (" + old(c) + " <=> " + new(c) + ")"
		}
	}
	return "updated_at = CASE WHEN " + strings.Join(diff, " OR ") + " THEN " + new("updated_at") + " ELSE " + old("updated_at") + " END"
}

// onConflict is the start of the update part of an upsert into hlstats_Bans.
func (s *Store) onConflict() string {
	if s.db.Postgres() {
		return " ON CONFLICT (steamid64, source) DO UPDATE SET "
	}
	return " ON DUPLICATE KEY UPDATE "
}

// newVal is the value of the insert of column c in the update part.
func (s *Store) newVal(c string) string {
	if s.db.Postgres() {
		return c + " = EXCLUDED." + c
	}
	return c + " = VALUES(" + c + ")"
}

// SaveSteamBans stores the answers of the Steam Web API (one row per
// account, also without a ban) as checked at now. last_ban is the day of
// the newest VAC or game ban (Steam gives the days since it).
func (s *Store) SaveSteamBans(ctx context.Context, bans []SteamBan, now time.Time) error {
	content := []string{"banned", "vac_banned", "vac_bans", "game_bans", "community_banned", "economy_ban", "last_ban"}
	sets := []string{s.changedAt(content...)}
	for _, c := range append(content, "days_since_last_ban", "last_checked") {
		sets = append(sets, s.newVal(c))
	}
	q := `INSERT INTO hlstats_Bans (steamid64, source, banned, vac_banned, vac_bans, game_bans, community_banned, economy_ban,
		days_since_last_ban, last_ban, first_seen, last_checked, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)` +
		s.onConflict() + strings.Join(sets, ", ")
	day := now.Truncate(24 * time.Hour).Unix()
	for _, b := range bans {
		var last int64
		if b.Banned() {
			last = max(0, day-int64(max(0, b.DaysSinceLastBan))*86400)
		}
		eco := strings.ToLower(clip(strings.TrimSpace(b.EconomyBan), 16))
		if eco == "" {
			eco = "none"
		}
		if err := s.run(ctx, step{q, []any{b.SteamID64, BanSourceSteam, boolInt(b.Banned()), boolInt(b.VACBanned), min(max(0, b.VACBans), 65535),
			min(max(0, b.GameBans), 65535), boolInt(b.CommunityBanned), eco, min(max(0, b.DaysSinceLastBan), 16777215), last,
			now.Unix(), now.Unix(), now.Unix()}}); err != nil {
			return err
		}
	}
	return nil
}

// nullTime is a Unix time, or NULL for the zero time.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return max(0, t.Unix())
}

// SaveSourceBans makes the SourceBans rows of hlstats_Bans the running bans
// of a completely read list: each is stored (checked at now) and the rows
// of accounts the list no longer shows as banned (lifted, expired, deleted)
// are deleted. It returns how many rows it deleted. Only call it with the
// whole list; a partly read list would delete running bans.
func (s *Store) SaveSourceBans(ctx context.Context, bans []SourceBan, now time.Time) (int, error) {
	content := []string{"sb_reason", "sb_length", "sb_created", "sb_ends", "sb_admin", "last_ban"}
	sets := []string{s.changedAt(content...)}
	for _, c := range append(content, "last_checked") {
		sets = append(sets, s.newVal(c))
	}
	q := `INSERT INTO hlstats_Bans (steamid64, source, banned, sb_reason, sb_length, sb_created, sb_ends, sb_admin, last_ban,
		first_seen, last_checked, updated_at) VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?)` + s.onConflict() + strings.Join(sets, ", ")
	keep := map[uint64]bool{}
	for _, b := range bans {
		keep[b.SteamID64] = true
		var last int64
		if !b.Created.IsZero() {
			last = max(0, b.Created.Unix())
		}
		if err := s.run(ctx, step{q, []any{b.SteamID64, BanSourceSourceBans, clip(b.Reason, 255), max(0, b.LengthMinutes),
			nullTime(b.Created), nullTime(b.Ends), clip(b.Admin, 128), last, now.Unix(), now.Unix(), now.Unix()}}); err != nil {
			return 0, err
		}
	}
	stored, err := s.accounts(ctx, `SELECT steamid64 FROM hlstats_Bans WHERE source = ?`, BanSourceSourceBans)
	if err != nil {
		return 0, err
	}
	var gone []uint64
	for _, id := range stored {
		if !keep[id] {
			gone = append(gone, id)
		}
	}
	return len(gone), s.deleteAccounts(ctx, BanSourceSourceBans, gone)
}

// DeleteExpiredSourceBans deletes the SourceBans rows whose end has passed,
// also while the list cannot be read. It returns how many it deleted.
func (s *Store) DeleteExpiredSourceBans(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.exec(ctx, `DELETE FROM hlstats_Bans WHERE source = ? AND sb_ends IS NOT NULL AND sb_ends <= ?`, BanSourceSourceBans, now.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// banChunk is how many accounts one IN list holds.
const banChunk = 500

func (s *Store) deleteAccounts(ctx context.Context, source string, ids []uint64) error {
	for part := range slices.Chunk(ids, banChunk) {
		args := []any{source}
		for _, id := range part {
			args = append(args, id)
		}
		if err := s.run(ctx, step{`DELETE FROM hlstats_Bans WHERE source = ? AND steamid64 IN (` + marks(len(part)) + `)`, args}); err != nil {
			return err
		}
	}
	return nil
}

func marks(n int) string { return strings.TrimSuffix(strings.Repeat("?, ", n), ", ") }

func (s *Store) accounts(ctx context.Context, q string, args ...any) ([]uint64, error) {
	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uint64
	for rows.Next() {
		var id uint64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SyncBanPlayers makes hlstats_Bans_Players the players of the banned
// accounts (hlstats_Bans.banned = 1) as players gives them (SteamID64 ->
// players of every ranking), and hlstats_Bans.playerId the lowest of them
// (NULL when no player has the account). It returns how many pairs it
// added and removed.
func (s *Store) SyncBanPlayers(ctx context.Context, players map[uint64][]BanPlayer) (added, removed int, err error) {
	banned, err := s.accounts(ctx, `SELECT DISTINCT steamid64 FROM hlstats_Bans WHERE banned = 1`)
	if err != nil {
		return 0, 0, err
	}
	type pair struct {
		id  uint64
		pid int64
	}
	want := map[pair]string{}
	for _, id := range banned {
		for _, p := range players[id] {
			want[pair{id, p.PlayerID}] = p.Game
		}
	}
	rows, err := s.query(ctx, `SELECT steamid64, playerId, game FROM hlstats_Bans_Players`)
	if err != nil {
		return 0, 0, err
	}
	have := map[pair]string{}
	for rows.Next() {
		var p pair
		var g string
		if err := rows.Scan(&p.id, &p.pid, &g); err != nil {
			rows.Close()
			return 0, 0, err
		}
		have[p] = g
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	for p, g := range have {
		if wg, ok := want[p]; ok && wg == g {
			continue
		}
		if err := s.run(ctx, step{`DELETE FROM hlstats_Bans_Players WHERE steamid64 = ? AND playerId = ?`, []any{p.id, p.pid}}); err != nil {
			return added, removed, err
		}
		removed++
	}
	for p, g := range want {
		if hg, ok := have[p]; ok && hg == g {
			continue
		}
		if err := s.run(ctx, step{s.ignore(`INSERT INTO hlstats_Bans_Players (steamid64, playerId, game) VALUES (?, ?, ?)`),
			[]any{p.id, p.pid, clip(g, 32)}}); err != nil {
			return added, removed, err
		}
		added++
	}
	// hlstats_Bans.playerId: the lowest player of the account.
	rows, err = s.query(ctx, `SELECT steamid64, playerId FROM hlstats_Bans WHERE banned = 1`)
	if err != nil {
		return added, removed, err
	}
	cur := map[uint64][]*int64{}
	for rows.Next() {
		var id uint64
		var pid *int64
		if err := rows.Scan(&id, &pid); err != nil {
			rows.Close()
			return added, removed, err
		}
		cur[id] = append(cur[id], pid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return added, removed, err
	}
	for id, pids := range cur {
		var lowest *int64
		for _, p := range players[id] {
			if lowest == nil || p.PlayerID < *lowest {
				v := p.PlayerID
				lowest = &v
			}
		}
		same := true
		for _, p := range pids {
			same = same && ((p == nil && lowest == nil) || (p != nil && lowest != nil && *p == *lowest))
		}
		if same {
			continue
		}
		var v any
		if lowest != nil {
			v = *lowest
		}
		if err := s.run(ctx, step{`UPDATE hlstats_Bans SET playerId = ? WHERE steamid64 = ? AND banned = 1`, []any{v, id}}); err != nil {
			return added, removed, err
		}
	}
	return added, removed, nil
}

// BanCounts returns how many accounts hlstats_Bans holds per source and
// how many of them are banned.
func (s *Store) BanCounts(ctx context.Context) (checked, steamBanned, sourceBans int, err error) {
	err = s.queryRow(ctx, `SELECT
		COALESCE(SUM(CASE WHEN source = 'steam' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN source = 'steam' AND banned = 1 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN source = 'sourcebans' THEN 1 ELSE 0 END), 0) FROM hlstats_Bans`).Scan(&checked, &steamBanned, &sourceBans)
	return
}
