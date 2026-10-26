// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import "context"

// BanTypeVAC is hlstats_Players_Bans.ban_type of a VAC ban on record that
// the VAC block of a ranking (gamewe_block_vac_<game>) kicked (the Steam
// Web API's GetPlayerBans).
const BanTypeVAC = "vac"

// HasRunningBan reports whether the player uniqueID has a running ban of
// the type in game.
func (s *Store) HasRunningBan(ctx context.Context, game, uniqueID, banType string) (bool, error) {
	var n int
	err := s.queryRow(ctx, `SELECT COUNT(*) FROM hlstats_Players_Bans WHERE game = ? AND unique_id = ? AND ban_type = ? AND active = 1`,
		game, clip(uniqueID, 64), banType).Scan(&n)
	return n > 0, err
}
