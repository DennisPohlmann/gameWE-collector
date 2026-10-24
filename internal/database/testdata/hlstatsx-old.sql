-- Test fixture: the table structure (no data) of an old HLstatsX database,
-- as gameWE found it before its first release: latin1 tables, no
-- rcon_password, realgame, server_id, kill positions, hlstats_Events_Latency
-- or hlstats_gamewe_jobs, single-column killerId / victimId keys. The
-- integration tests load it, add testdata/hlstatsx-old-data.sql and run
-- gamewe db migrate on it.


SET SQL_MODE = "NO_AUTO_VALUE_ON_ZERO";
SET time_zone = "+00:00";

CREATE TABLE `hlstats_Actions` (
  `id` smallint(5) UNSIGNED NOT NULL,
  `game` varchar(10) NOT NULL DEFAULT 'valve',
  `code` varchar(128) NOT NULL DEFAULT '',
  `reward_player` mediumint(9) NOT NULL DEFAULT 10,
  `reward_team` mediumint(9) NOT NULL DEFAULT 0,
  `team` varchar(128) NOT NULL DEFAULT '',
  `description` varchar(255) DEFAULT NULL,
  `for_PlayerActions` enum('0','1') NOT NULL DEFAULT '0',
  `for_PlayerPlayerActions` enum('0','1') NOT NULL DEFAULT '0',
  `for_TeamActions` enum('0','1') NOT NULL DEFAULT '0',
  `for_WorldActions` enum('0','1') NOT NULL DEFAULT '0',
  `count` bigint(20) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Awards` (
  `awardId` mediumint(8) UNSIGNED NOT NULL,
  `awardType` enum('W','O') NOT NULL DEFAULT 'W',
  `game` varchar(10) NOT NULL DEFAULT 'valve',
  `code` varchar(128) NOT NULL DEFAULT '',
  `name` varchar(255) NOT NULL DEFAULT '',
  `verb` varchar(255) NOT NULL DEFAULT '',
  `d_winner_id` mediumint(8) UNSIGNED DEFAULT NULL,
  `d_winner_count` mediumint(8) UNSIGNED DEFAULT NULL
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Awards_History` (
  `id` mediumint(8) UNSIGNED NOT NULL,
  `award_id` mediumint(8) UNSIGNED DEFAULT NULL,
  `winner_id` mediumint(8) UNSIGNED DEFAULT NULL,
  `date` varchar(50) DEFAULT NULL,
  `winner_count` mediumint(8) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Clans` (
  `clanId` mediumint(8) UNSIGNED NOT NULL,
  `tag` varchar(128) NOT NULL DEFAULT '',
  `name` varchar(255) NOT NULL DEFAULT '',
  `homepage` varchar(255) NOT NULL DEFAULT '',
  `game` varchar(10) NOT NULL DEFAULT '',
  `flag` varchar(255) NOT NULL DEFAULT ''
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_ClanTags` (
  `id` mediumint(8) UNSIGNED NOT NULL,
  `pattern` varchar(255) NOT NULL DEFAULT '',
  `position` varchar(255) NOT NULL DEFAULT 'EITHER',
  `pattern_length` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `sort_id` mediumint(8) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Definition_Ranks_Ribbons` (
  `id` mediumint(8) UNSIGNED NOT NULL,
  `definition_type` varchar(10) NOT NULL DEFAULT 'ranks',
  `game_code` varchar(10) NOT NULL DEFAULT 'valve',
  `code` varchar(128) NOT NULL DEFAULT '',
  `name` varchar(128) NOT NULL DEFAULT '',
  `img_file` varchar(128) NOT NULL DEFAULT '',
  `thumb_file` varchar(128) NOT NULL DEFAULT '',
  `minimum` mediumint(8) UNSIGNED DEFAULT 0,
  `restriction_type` varchar(16) NOT NULL DEFAULT 'kills'
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Definition_Ribbons_Classes` (
  `id` mediumint(8) UNSIGNED NOT NULL,
  `game_code` varchar(10) NOT NULL DEFAULT 'valve',
  `name` varchar(128) NOT NULL DEFAULT '',
  `maximum` mediumint(8) UNSIGNED DEFAULT 0,
  `steps` mediumint(8) UNSIGNED DEFAULT 0,
  `class` mediumint(8) UNSIGNED DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Events_Admin` (
  `timestamp_id` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `game_id` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `map` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `type` varchar(255) NOT NULL DEFAULT 'Unknown',
  `message` text NOT NULL,
  `playerName` varchar(255) NOT NULL DEFAULT ''
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Events_ChangeName` (
  `timestamp_id` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `game_id` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `map` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `playerId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `oldName` varchar(255) NOT NULL DEFAULT '',
  `newName` varchar(255) NOT NULL DEFAULT ''
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Events_ChangeRole` (
  `timestamp_id` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `game_id` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `map` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `playerId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `role` tinyint(3) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Events_ChangeTeam` (
  `timestamp_id` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `game_id` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `map` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `playerId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `team` tinyint(3) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Events_Chat` (
  `timestamp_id` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `game_id` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `server_id` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `playerId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `message_mode` tinyint(4) NOT NULL DEFAULT 0,
  `message` varchar(4096) NOT NULL DEFAULT ''
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Events_Connects` (
  `timestamp_id` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `game_id` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `map` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `playerId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `ipAddress` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `hostname` varchar(255) NOT NULL DEFAULT '',
  `hostgroup` varchar(255) NOT NULL DEFAULT ''
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Events_Disconnects` (
  `timestamp_id` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `game_id` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `map` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `playerId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Events_Entries` (
  `timestamp_id` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `game_id` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `map` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `playerId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Events_Frags` (
  `timestamp_id` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `game_id` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `map` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `killerId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `victimId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `weapon` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `headshot` tinyint(4) NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Events_Locations` (
  `day` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `map` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `type` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `playerId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `loc_1` smallint(6) NOT NULL DEFAULT 0,
  `loc_2` smallint(6) NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Events_PlayerActions` (
  `timestamp_id` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `game_id` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `map` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `playerId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `actionId` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `bonus` smallint(6) NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Events_PlayerPlayerActions` (
  `timestamp_id` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `game_id` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `map` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `playerId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `victimId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `actionId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `bonus` smallint(6) NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Events_Rcon` (
  `timestamp_id` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `game_id` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `map` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `type` varchar(6) NOT NULL DEFAULT 'UNK',
  `remoteIp` varchar(32) NOT NULL DEFAULT '',
  `password` varchar(255) NOT NULL DEFAULT '',
  `command` varchar(255) NOT NULL DEFAULT ''
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Events_Suicides` (
  `timestamp_id` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `game_id` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `map` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `playerId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `weapon` smallint(5) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Events_TeamBonuses` (
  `timestamp_id` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `game_id` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `map` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `playerId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `actionId` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `bonus` tinyint(4) NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Events_Teamkills` (
  `timestamp_id` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `game_id` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `map` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `killerId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `victimId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `weapon` smallint(5) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Games` (
  `id` tinyint(3) UNSIGNED NOT NULL,
  `code` char(10) NOT NULL DEFAULT '',
  `name` varchar(255) NOT NULL DEFAULT '',
  `hidden` enum('0','1') NOT NULL DEFAULT '0',
  `display_id` smallint(6) NOT NULL DEFAULT 0,
  `weapon_images` varchar(32) NOT NULL DEFAULT 'default'
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_HostGroups` (
  `id` mediumint(8) UNSIGNED NOT NULL,
  `pattern` varchar(255) NOT NULL DEFAULT '',
  `name` varchar(255) NOT NULL DEFAULT ''
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Livestats` (
  `player_id` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `server_id` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `cli_address_id` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `cli_location_id` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `unique_id` char(32) NOT NULL DEFAULT '',
  `name` char(128) NOT NULL DEFAULT '',
  `team` char(64) NOT NULL DEFAULT '',
  `squad` char(64) NOT NULL DEFAULT '',
  `kills` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `deaths` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `suicides` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `headshots` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `shots` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `hits` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `is_dead` tinyint(4) NOT NULL DEFAULT 0,
  `has_bomb` tinyint(4) NOT NULL DEFAULT 0,
  `ping` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `connected` int(11) NOT NULL DEFAULT 0,
  `skill_change` mediumint(9) NOT NULL DEFAULT 0,
  `skill` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `hpk` float(5,2) NOT NULL DEFAULT 0.00,
  `kpd` float(7,2) NOT NULL DEFAULT 0.00,
  `kill_assists` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `kill_assists_medic` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `kills_assisted` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `rounds` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `survived_count` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `survived` float(8,2) UNSIGNED NOT NULL DEFAULT 0.00,
  `kill_streak` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `death_streak` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `wins` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `losses` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `custom_wins` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `death_headshots` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `team_kills` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `team_killed` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `death_hpk` float(5,2) NOT NULL DEFAULT 0.00,
  `player_hidden` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `points_healed` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `flags_captured` smallint(5) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MEMORY DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Maps` (
  `id` mediumint(8) UNSIGNED NOT NULL,
  `game` varchar(10) NOT NULL DEFAULT '',
  `map` varchar(96) NOT NULL DEFAULT '',
  `kills` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `headshots` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `teamkills` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `teama_wins` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `teamb_wins` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `played` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `play_time` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `rounds` int(10) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Options` (
  `keyname` varchar(128) NOT NULL DEFAULT '',
  `value` varchar(255) NOT NULL DEFAULT ''
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_PlayerNames` (
  `playerId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `name` varchar(128) NOT NULL DEFAULT '',
  `lastuse` datetime NOT NULL DEFAULT '0000-00-00 00:00:00',
  `connection_time` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `numuses` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kills` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `deaths` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `suicides` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `headshots` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `shots` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `hits` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `rounds` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `survived_count` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `survived` float(8,2) UNSIGNED NOT NULL DEFAULT 0.00,
  `wins` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `losses` mediumint(8) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Players` (
  `playerId` mediumint(8) UNSIGNED NOT NULL,
  `last_event` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `connection_time` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `clan` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kills` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `deaths` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `suicides` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `skill` mediumint(8) UNSIGNED NOT NULL DEFAULT 1000,
  `shots` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `hits` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `game` char(10) NOT NULL DEFAULT '',
  `hideranking` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `headshots` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `last_skill_change` mediumint(9) NOT NULL DEFAULT 0,
  `hpk` float(5,2) NOT NULL DEFAULT 0.00,
  `kpd` float(7,2) NOT NULL DEFAULT 0.00,
  `sort_skill` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `sort_kpd` float(7,2) NOT NULL DEFAULT 0.00,
  `kill_assists` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kill_assists_medic` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kills_assisted` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `rounds` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `survived_count` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `survived` float(8,2) UNSIGNED NOT NULL DEFAULT 0.00,
  `kill_streak` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `death_streak` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `wins` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `losses` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `custom_wins` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `rank` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `game_id` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `death_headshots` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `team_kills` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `team_killed` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `death_hpk` float(5,2) NOT NULL DEFAULT 0.00,
  `points_healed` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `flags_captured` mediumint(8) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_PlayersInactive` (
  `playerId` mediumint(8) UNSIGNED NOT NULL,
  `last_event` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `connection_time` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `clan` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kills` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `deaths` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `suicides` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `skill` mediumint(8) UNSIGNED NOT NULL DEFAULT 1000,
  `shots` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `hits` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `game` char(10) NOT NULL DEFAULT '',
  `hideranking` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `headshots` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `last_skill_change` mediumint(9) NOT NULL DEFAULT 0,
  `hpk` float(5,2) NOT NULL DEFAULT 0.00,
  `kpd` float(7,2) NOT NULL DEFAULT 0.00,
  `sort_skill` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `sort_kpd` float(7,2) NOT NULL DEFAULT 0.00,
  `kill_assists` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kill_assists_medic` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kills_assisted` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `rounds` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `survived_count` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `survived` float(8,2) UNSIGNED NOT NULL DEFAULT 0.00,
  `kill_streak` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `death_streak` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `wins` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `losses` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `custom_wins` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `rank` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `game_id` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `death_headshots` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `team_kills` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `team_killed` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `death_hpk` float(5,2) NOT NULL DEFAULT 0.00,
  `points_healed` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `flags_captured` mediumint(8) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Players_Actions` (
  `player_id` mediumint(8) UNSIGNED NOT NULL,
  `yearweek_id` mediumint(8) UNSIGNED NOT NULL,
  `game_id` tinyint(3) UNSIGNED NOT NULL,
  `action_id` smallint(5) UNSIGNED NOT NULL,
  `count` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `points` mediumint(9) NOT NULL DEFAULT 0,
  `daily_award` smallint(5) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Players_Actions_History_Months` (
  `player_id` mediumint(8) UNSIGNED NOT NULL,
  `year_id` smallint(5) UNSIGNED NOT NULL,
  `month_id` tinyint(3) UNSIGNED NOT NULL,
  `game_id` tinyint(3) UNSIGNED NOT NULL,
  `action_id` smallint(5) UNSIGNED NOT NULL,
  `count` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `points` int(11) NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Players_Data` (
  `playerId` mediumint(8) UNSIGNED NOT NULL,
  `first_connect` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `last_connect` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `total_connects` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `lastName` varchar(128) NOT NULL DEFAULT '',
  `last_address_id` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `location_id` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `average_ping_count` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `average_ping` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `award_ping` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `bans_on_record` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `custom_ban_type` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `custom_ban_reason` varchar(255) NOT NULL DEFAULT '',
  `fullName` varchar(255) NOT NULL DEFAULT '',
  `email` varchar(255) NOT NULL DEFAULT '',
  `homepage` varchar(255) NOT NULL DEFAULT '',
  `icq` varchar(64) NOT NULL DEFAULT '',
  `connect_type` tinyint(3) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Players_History` (
  `playerId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `eventTime` date NOT NULL DEFAULT '0000-00-00',
  `connection_time` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kills` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `deaths` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `suicides` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `skill` mediumint(8) UNSIGNED NOT NULL DEFAULT 1000,
  `shots` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `hits` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `game` char(10) NOT NULL DEFAULT '',
  `headshots` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `skill_change` mediumint(9) NOT NULL DEFAULT 0,
  `kill_assists` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `kill_assists_medic` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `kills_assisted` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `rounds` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `survived_count` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `survived` float(8,2) UNSIGNED NOT NULL DEFAULT 0.00,
  `kill_streak` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `death_streak` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `wins` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `losses` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `custom_wins` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `team_kills` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `points_healed` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `flags_captured` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `damage` int(10) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Players_Locations` (
  `id` mediumint(8) UNSIGNED NOT NULL,
  `city` varchar(255) NOT NULL DEFAULT '',
  `state` varchar(255) NOT NULL DEFAULT '',
  `country` varchar(255) NOT NULL DEFAULT '',
  `flag` varchar(255) NOT NULL DEFAULT '',
  `lat` varchar(32) NOT NULL DEFAULT '',
  `lng` varchar(32) NOT NULL DEFAULT ''
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Players_Maps` (
  `player_id` mediumint(8) UNSIGNED NOT NULL,
  `yearweek_id` mediumint(8) UNSIGNED NOT NULL,
  `game_id` tinyint(3) UNSIGNED NOT NULL,
  `map_id` smallint(5) UNSIGNED NOT NULL,
  `time_played` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `count_played` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `last_played` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `last_location_kill` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `skill_change` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kills` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `deaths` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `suicides` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `headshots` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `shots` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `hits` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `kill_assists` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `kill_assists_medic` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `kills_assisted` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `rounds` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `survived_count` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `survived` float(8,2) UNSIGNED NOT NULL DEFAULT 0.00,
  `kill_streak` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `death_streak` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `wins` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `losses` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `custom_wins` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `points_healed` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `flags_captured` smallint(5) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Players_Maps_History_Months` (
  `player_id` mediumint(8) UNSIGNED NOT NULL,
  `year_id` smallint(5) UNSIGNED NOT NULL,
  `month_id` tinyint(3) UNSIGNED NOT NULL,
  `game_id` tinyint(3) UNSIGNED NOT NULL,
  `map_id` smallint(5) UNSIGNED NOT NULL,
  `time_played` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `count_played` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `last_played` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `last_location_kill` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `skill_change` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kills` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `deaths` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `suicides` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `headshots` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `shots` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `hits` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `kill_assists` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kill_assists_medic` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kills_assisted` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `rounds` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `survived_count` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `survived` float(8,2) UNSIGNED NOT NULL DEFAULT 0.00,
  `kill_streak` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `death_streak` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `wins` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `losses` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `custom_wins` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `points_healed` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `flags_captured` smallint(5) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Players_Resets` (
  `player_id` mediumint(8) UNSIGNED NOT NULL,
  `timestamp` int(10) UNSIGNED NOT NULL
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Players_Roles` (
  `player_id` mediumint(8) UNSIGNED NOT NULL,
  `yearweek_id` mediumint(8) UNSIGNED NOT NULL,
  `game_id` tinyint(3) UNSIGNED NOT NULL,
  `role_id` smallint(5) UNSIGNED NOT NULL,
  `time_played` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `count_played` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `skill_change` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kills` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `deaths` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `suicides` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `headshots` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `shots` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `hits` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `kill_assists` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `kill_assists_medic` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `kills_assisted` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `rounds` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `survived_count` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `survived` float(8,2) UNSIGNED NOT NULL DEFAULT 0.00,
  `wins` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `losses` smallint(5) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Players_Roles_History_Months` (
  `player_id` mediumint(8) UNSIGNED NOT NULL,
  `year_id` smallint(5) UNSIGNED NOT NULL,
  `month_id` tinyint(3) UNSIGNED NOT NULL,
  `game_id` tinyint(3) UNSIGNED NOT NULL,
  `role_id` smallint(5) UNSIGNED NOT NULL,
  `time_played` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `count_played` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `skill_change` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kills` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `deaths` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `suicides` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `headshots` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `shots` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `hits` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `kill_assists` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kill_assists_medic` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kills_assisted` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `rounds` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `survived_count` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `survived` float(8,2) UNSIGNED NOT NULL DEFAULT 0.00,
  `wins` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `losses` smallint(5) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Players_Teams` (
  `player_id` mediumint(8) UNSIGNED NOT NULL,
  `yearweek_id` mediumint(8) UNSIGNED NOT NULL,
  `game_id` tinyint(3) UNSIGNED NOT NULL,
  `team_id` smallint(5) UNSIGNED NOT NULL,
  `time_played` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `count_played` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `skill_change` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kills` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `deaths` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `suicides` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `headshots` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `shots` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `hits` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `kill_assists` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `kill_assists_medic` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `kills_assisted` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `rounds` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `survived_count` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `survived` float(8,2) UNSIGNED NOT NULL DEFAULT 0.00,
  `wins` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `losses` smallint(5) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Players_Teams_History_Months` (
  `player_id` mediumint(8) UNSIGNED NOT NULL,
  `year_id` smallint(5) UNSIGNED NOT NULL,
  `month_id` tinyint(3) UNSIGNED NOT NULL,
  `game_id` tinyint(3) UNSIGNED NOT NULL,
  `team_id` smallint(5) UNSIGNED NOT NULL,
  `time_played` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `count_played` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `skill_change` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kills` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `deaths` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `suicides` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `headshots` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `shots` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `hits` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `kill_assists` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kill_assists_medic` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `kills_assisted` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `rounds` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `survived_count` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `survived` float(8,2) UNSIGNED NOT NULL DEFAULT 0.00,
  `wins` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `losses` smallint(5) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_Players_Weapons` (
  `player_id` mediumint(8) UNSIGNED NOT NULL,
  `yearweek_id` mediumint(8) UNSIGNED NOT NULL,
  `game_id` tinyint(3) UNSIGNED NOT NULL,
  `weapon_id` smallint(5) UNSIGNED NOT NULL,
  `shots` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `hits` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `headshots` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `damage` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `kills` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `deaths` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `head` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `chest` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `stomach` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `leftarm` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `rightarm` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `leftleg` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `rightleg` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `daily_award` smallint(5) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Players_Weapons_History_Months` (
  `player_id` mediumint(8) UNSIGNED NOT NULL,
  `year_id` smallint(5) UNSIGNED NOT NULL,
  `month_id` tinyint(3) UNSIGNED NOT NULL,
  `game_id` tinyint(3) UNSIGNED NOT NULL,
  `weapon_id` smallint(5) UNSIGNED NOT NULL,
  `shots` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `hits` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `headshots` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `damage` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `kills` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `deaths` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `head` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `chest` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `stomach` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `leftarm` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `rightarm` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `leftleg` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `rightleg` smallint(5) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci PACK_KEYS=1;

CREATE TABLE `hlstats_PlayerUniqueIds` (
  `playerId` mediumint(8) UNSIGNED NOT NULL DEFAULT 0,
  `uniqueId` varchar(255) NOT NULL DEFAULT '',
  `game` varchar(10) NOT NULL DEFAULT ''
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Roles` (
  `roleId` tinyint(3) UNSIGNED NOT NULL,
  `game` varchar(10) NOT NULL DEFAULT 'valve',
  `code` varchar(128) NOT NULL DEFAULT '',
  `name` varchar(255) NOT NULL DEFAULT '',
  `hidden` enum('0','1') NOT NULL DEFAULT '0',
  `time_played` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `kills` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `deaths` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `headshots` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `kill_assists` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `flags_captured` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `rounds` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `survived` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `wins` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `losses` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `custom_wins` bigint(20) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Servers` (
  `serverId` smallint(5) UNSIGNED NOT NULL,
  `address` varchar(32) NOT NULL DEFAULT '',
  `port` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `name` varchar(255) NOT NULL DEFAULT '',
  `game` varchar(10) NOT NULL DEFAULT 'valve',
  `publicaddress` varchar(255) NOT NULL DEFAULT '',
  `statusurl` varchar(255) NOT NULL DEFAULT '',
  `kills` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `players` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `rounds` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `suicides` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `headshots` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `bombs_planted` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `bombs_defused` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `ct_wins` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `ts_wins` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `act_players` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `max_players` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `act_map` varchar(255) NOT NULL DEFAULT '',
  `map_rounds` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `map_ct_wins` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `map_ts_wins` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `map_started` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `map_changes` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `ct_shots` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `ct_hits` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `ts_shots` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `ts_hits` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `map_ct_shots` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `map_ct_hits` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `map_ts_shots` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `map_ts_hits` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `display_id` smallint(6) NOT NULL DEFAULT 0,
  `lat` varchar(8) NOT NULL DEFAULT '',
  `lng` varchar(8) NOT NULL DEFAULT '',
  `country_code` varchar(4) NOT NULL DEFAULT '',
  `country_name` varchar(32) NOT NULL DEFAULT '',
  `city` varchar(32) NOT NULL DEFAULT '',
  `state` varchar(32) NOT NULL DEFAULT '',
  `fixed_location` tinyint(4) NOT NULL DEFAULT 0,
  `account_id` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `vac_status` varchar(128) NOT NULL DEFAULT '',
  `steam_status` varchar(128) NOT NULL DEFAULT ''
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_server_load` (
  `server_id` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `timestamp` int(11) NOT NULL DEFAULT 0,
  `act_players` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `min_players` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `max_players` tinyint(3) UNSIGNED NOT NULL DEFAULT 0,
  `map` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `cpu` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `fps` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `uptime` smallint(5) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Style` (
  `keyname` varchar(32) NOT NULL DEFAULT '',
  `def` varchar(255) NOT NULL DEFAULT '',
  `black` varchar(255) NOT NULL DEFAULT '',
  `light_blue` varchar(255) NOT NULL DEFAULT '',
  `grey` varchar(255) NOT NULL DEFAULT '',
  `ua_style` varchar(255) NOT NULL DEFAULT '',
  `red` varchar(255) NOT NULL DEFAULT '',
  `light_grey` varchar(255) NOT NULL DEFAULT '',
  `white` varchar(255) NOT NULL DEFAULT ''
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Teams` (
  `teamId` tinyint(3) UNSIGNED NOT NULL,
  `game` varchar(10) NOT NULL DEFAULT 'valve',
  `code` varchar(128) NOT NULL DEFAULT '',
  `name` varchar(255) NOT NULL DEFAULT '',
  `hidden` enum('0','1') NOT NULL DEFAULT '0',
  `time_played` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `kills` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `deaths` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `headshots` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `kill_assists` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `flags_captured` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `rounds` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `survived` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `wins` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `losses` bigint(20) UNSIGNED NOT NULL DEFAULT 0,
  `custom_wins` bigint(20) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Trend` (
  `timestamp` int(11) NOT NULL DEFAULT 0,
  `game` char(10) NOT NULL DEFAULT '',
  `players` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `kills` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `headshots` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `servers` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `act_slots` smallint(5) UNSIGNED NOT NULL DEFAULT 0,
  `max_slots` smallint(5) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Users` (
  `username` varchar(128) NOT NULL DEFAULT '',
  `password` varchar(255) NOT NULL DEFAULT '',
  `acclevel` int(11) NOT NULL DEFAULT 0,
  `playerId` int(11) NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

CREATE TABLE `hlstats_Weapons` (
  `weaponId` smallint(5) UNSIGNED NOT NULL,
  `game` varchar(10) NOT NULL DEFAULT 'valve',
  `code` varchar(128) NOT NULL DEFAULT '',
  `name` varchar(255) NOT NULL DEFAULT '',
  `modifier` float(10,2) NOT NULL DEFAULT 1.00,
  `kills` int(10) UNSIGNED NOT NULL DEFAULT 0,
  `headshots` int(10) UNSIGNED NOT NULL DEFAULT 0
) ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=latin1_swedish_ci;

ALTER TABLE `hlstats_Actions`
  ADD PRIMARY KEY (`id`),
  ADD UNIQUE KEY `game` (`game`,`code`),
  ADD KEY `code` (`code`);

ALTER TABLE `hlstats_Awards`
  ADD PRIMARY KEY (`awardId`),
  ADD UNIQUE KEY `game` (`game`,`code`),
  ADD KEY `name` (`name`);

ALTER TABLE `hlstats_Awards_History`
  ADD PRIMARY KEY (`id`),
  ADD KEY `winner_id` (`winner_id`);

ALTER TABLE `hlstats_Clans`
  ADD PRIMARY KEY (`clanId`),
  ADD KEY `game` (`game`),
  ADD KEY `tag` (`tag`,`game`);

ALTER TABLE `hlstats_ClanTags`
  ADD PRIMARY KEY (`id`),
  ADD UNIQUE KEY `pattern` (`pattern`),
  ADD KEY `pattern_length` (`pattern_length`,`sort_id`);

ALTER TABLE `hlstats_Definition_Ranks_Ribbons`
  ADD PRIMARY KEY (`id`),
  ADD UNIQUE KEY `game_and_definition` (`game_code`,`definition_type`,`name`,`code`);

ALTER TABLE `hlstats_Definition_Ribbons_Classes`
  ADD PRIMARY KEY (`id`),
  ADD UNIQUE KEY `game` (`game_code`,`class`);

ALTER TABLE `hlstats_Events_ChangeName`
  ADD KEY `playerId` (`playerId`),
  ADD KEY `timestamp_id` (`timestamp_id`);

ALTER TABLE `hlstats_Events_ChangeRole`
  ADD KEY `playerId` (`playerId`),
  ADD KEY `timestamp_id` (`timestamp_id`);

ALTER TABLE `hlstats_Events_ChangeTeam`
  ADD KEY `playerId` (`playerId`),
  ADD KEY `timestamp_id` (`timestamp_id`);

ALTER TABLE `hlstats_Events_Chat`
  ADD KEY `playerId` (`playerId`),
  ADD KEY `server_id` (`server_id`),
  ADD KEY `timestamp_id` (`timestamp_id`);

ALTER TABLE `hlstats_Events_Connects`
  ADD KEY `playerId` (`playerId`),
  ADD KEY `ipAddress` (`ipAddress`),
  ADD KEY `timestamp_id` (`timestamp_id`);

ALTER TABLE `hlstats_Events_Disconnects`
  ADD KEY `playerId` (`playerId`),
  ADD KEY `timestamp_id` (`timestamp_id`);

ALTER TABLE `hlstats_Events_Entries`
  ADD KEY `playerId` (`playerId`),
  ADD KEY `timestamp_id` (`timestamp_id`);

ALTER TABLE `hlstats_Events_Frags`
  ADD KEY `timestamp_id` (`timestamp_id`),
  ADD KEY `killerId` (`killerId`),
  ADD KEY `victimId` (`victimId`);

ALTER TABLE `hlstats_Events_Locations`
  ADD KEY `map` (`map`),
  ADD KEY `playerId` (`playerId`),
  ADD KEY `day` (`day`);

ALTER TABLE `hlstats_Events_PlayerActions`
  ADD KEY `timestamp_id` (`timestamp_id`),
  ADD KEY `playerId` (`playerId`);

ALTER TABLE `hlstats_Events_PlayerPlayerActions`
  ADD KEY `timestamp_id` (`timestamp_id`),
  ADD KEY `playerId` (`playerId`),
  ADD KEY `victimId` (`victimId`);

ALTER TABLE `hlstats_Events_Rcon`
  ADD KEY `timestamp_id` (`timestamp_id`);

ALTER TABLE `hlstats_Events_Suicides`
  ADD KEY `playerId` (`playerId`),
  ADD KEY `timestamp_id` (`timestamp_id`);

ALTER TABLE `hlstats_Events_TeamBonuses`
  ADD KEY `timestamp_id` (`timestamp_id`),
  ADD KEY `playerId` (`playerId`);

ALTER TABLE `hlstats_Events_Teamkills`
  ADD KEY `killerId` (`killerId`),
  ADD KEY `victimId` (`victimId`);

ALTER TABLE `hlstats_Games`
  ADD PRIMARY KEY (`id`),
  ADD UNIQUE KEY `code` (`code`),
  ADD KEY `display_id` (`display_id`,`code`);

ALTER TABLE `hlstats_HostGroups`
  ADD PRIMARY KEY (`id`);

ALTER TABLE `hlstats_Livestats`
  ADD PRIMARY KEY (`player_id`),
  ADD KEY `kills` (`kills`) USING BTREE;

ALTER TABLE `hlstats_Maps`
  ADD PRIMARY KEY (`id`),
  ADD UNIQUE KEY `game` (`game`,`map`);

ALTER TABLE `hlstats_Options`
  ADD PRIMARY KEY (`keyname`);

ALTER TABLE `hlstats_PlayerNames`
  ADD UNIQUE KEY `playerId` (`playerId`,`name`),
  ADD KEY `name` (`name`);

ALTER TABLE `hlstats_Players`
  ADD PRIMARY KEY (`playerId`),
  ADD KEY `sort_skill` (`game_id`,`sort_skill`,`sort_kpd`,`playerId`),
  ADD KEY `clan` (`clan`),
  ADD KEY `game` (`game`),
  ADD KEY `game_id` (`game_id`,`rank`),
  ADD KEY `kills` (`kills`);

ALTER TABLE `hlstats_PlayersInactive`
  ADD UNIQUE KEY `playerId` (`playerId`),
  ADD KEY `game` (`game`),
  ADD KEY `game_id` (`game_id`,`hideranking`),
  ADD KEY `clan` (`clan`);

ALTER TABLE `hlstats_Players_Actions`
  ADD UNIQUE KEY `player_id` (`player_id`,`yearweek_id`,`game_id`,`action_id`),
  ADD KEY `action_id` (`action_id`,`count`),
  ADD KEY `game_id` (`game_id`,`count`);

ALTER TABLE `hlstats_Players_Actions_History_Months`
  ADD UNIQUE KEY `player_id` (`player_id`,`year_id`,`month_id`,`game_id`,`action_id`),
  ADD KEY `action_id` (`action_id`,`count`);

ALTER TABLE `hlstats_Players_Data`
  ADD PRIMARY KEY (`playerId`),
  ADD KEY `first_connect` (`first_connect`),
  ADD KEY `location_id` (`location_id`),
  ADD KEY `connect_type` (`connect_type`);

ALTER TABLE `hlstats_Players_History`
  ADD UNIQUE KEY `playerId` (`playerId`,`eventTime`,`game`),
  ADD KEY `eventTime` (`eventTime`,`game`);

ALTER TABLE `hlstats_Players_Locations`
  ADD PRIMARY KEY (`id`),
  ADD UNIQUE KEY `geo` (`lat`,`lng`),
  ADD KEY `flag` (`flag`),
  ADD KEY `country` (`country`),
  ADD KEY `city` (`city`);

ALTER TABLE `hlstats_Players_Maps`
  ADD UNIQUE KEY `player_id` (`player_id`,`yearweek_id`,`game_id`,`map_id`),
  ADD KEY `map_id` (`map_id`,`kills`),
  ADD KEY `game_id` (`game_id`,`kills`);

ALTER TABLE `hlstats_Players_Maps_History_Months`
  ADD UNIQUE KEY `player_id` (`player_id`,`year_id`,`month_id`,`game_id`,`map_id`),
  ADD KEY `map_id` (`map_id`);

ALTER TABLE `hlstats_Players_Resets`
  ADD KEY `player_id` (`player_id`);

ALTER TABLE `hlstats_Players_Roles`
  ADD UNIQUE KEY `player_id` (`player_id`,`yearweek_id`,`game_id`,`role_id`),
  ADD KEY `role_id` (`role_id`);

ALTER TABLE `hlstats_Players_Roles_History_Months`
  ADD UNIQUE KEY `player_id` (`player_id`,`year_id`,`month_id`,`game_id`,`role_id`),
  ADD KEY `role_id` (`role_id`);

ALTER TABLE `hlstats_Players_Teams`
  ADD UNIQUE KEY `player_id` (`player_id`,`yearweek_id`,`game_id`,`team_id`),
  ADD KEY `team_id` (`team_id`);

ALTER TABLE `hlstats_Players_Teams_History_Months`
  ADD UNIQUE KEY `player_id` (`player_id`,`year_id`,`month_id`,`game_id`,`team_id`),
  ADD KEY `team_id` (`team_id`);

ALTER TABLE `hlstats_Players_Weapons`
  ADD UNIQUE KEY `player_id` (`player_id`,`yearweek_id`,`game_id`,`weapon_id`),
  ADD KEY `weapon_id` (`weapon_id`,`kills`),
  ADD KEY `game_id` (`game_id`,`kills`);

ALTER TABLE `hlstats_Players_Weapons_History_Months`
  ADD UNIQUE KEY `player_id` (`player_id`,`year_id`,`month_id`,`game_id`,`weapon_id`),
  ADD KEY `weapon_id` (`weapon_id`,`kills`);

ALTER TABLE `hlstats_PlayerUniqueIds`
  ADD PRIMARY KEY (`playerId`),
  ADD UNIQUE KEY `uniqueId` (`uniqueId`,`game`);

ALTER TABLE `hlstats_Roles`
  ADD PRIMARY KEY (`roleId`),
  ADD UNIQUE KEY `game` (`game`,`code`);

ALTER TABLE `hlstats_Servers`
  ADD PRIMARY KEY (`serverId`),
  ADD UNIQUE KEY `address` (`address`,`port`),
  ADD KEY `game` (`game`),
  ADD KEY `display_id` (`display_id`,`serverId`),
  ADD KEY `account_id` (`account_id`);

ALTER TABLE `hlstats_server_load`
  ADD KEY `server_id` (`server_id`),
  ADD KEY `timestamp` (`timestamp`);

ALTER TABLE `hlstats_Style`
  ADD PRIMARY KEY (`keyname`);

ALTER TABLE `hlstats_Teams`
  ADD PRIMARY KEY (`teamId`),
  ADD UNIQUE KEY `game` (`game`,`code`);

ALTER TABLE `hlstats_Trend`
  ADD KEY `timestamp` (`timestamp`);

ALTER TABLE `hlstats_Users`
  ADD PRIMARY KEY (`username`);

ALTER TABLE `hlstats_Weapons`
  ADD PRIMARY KEY (`weaponId`),
  ADD UNIQUE KEY `game` (`game`,`code`),
  ADD KEY `code` (`code`);

ALTER TABLE `hlstats_Actions`
  MODIFY `id` smallint(5) UNSIGNED NOT NULL AUTO_INCREMENT;

ALTER TABLE `hlstats_Awards`
  MODIFY `awardId` mediumint(8) UNSIGNED NOT NULL AUTO_INCREMENT;

ALTER TABLE `hlstats_Awards_History`
  MODIFY `id` mediumint(8) UNSIGNED NOT NULL AUTO_INCREMENT;

ALTER TABLE `hlstats_Clans`
  MODIFY `clanId` mediumint(8) UNSIGNED NOT NULL AUTO_INCREMENT;

ALTER TABLE `hlstats_ClanTags`
  MODIFY `id` mediumint(8) UNSIGNED NOT NULL AUTO_INCREMENT;

ALTER TABLE `hlstats_Definition_Ranks_Ribbons`
  MODIFY `id` mediumint(8) UNSIGNED NOT NULL AUTO_INCREMENT;

ALTER TABLE `hlstats_Definition_Ribbons_Classes`
  MODIFY `id` mediumint(8) UNSIGNED NOT NULL AUTO_INCREMENT;

ALTER TABLE `hlstats_Games`
  MODIFY `id` tinyint(3) UNSIGNED NOT NULL AUTO_INCREMENT;

ALTER TABLE `hlstats_HostGroups`
  MODIFY `id` mediumint(8) UNSIGNED NOT NULL AUTO_INCREMENT;

ALTER TABLE `hlstats_Maps`
  MODIFY `id` mediumint(8) UNSIGNED NOT NULL AUTO_INCREMENT;

ALTER TABLE `hlstats_Players`
  MODIFY `playerId` mediumint(8) UNSIGNED NOT NULL AUTO_INCREMENT;

ALTER TABLE `hlstats_Players_Locations`
  MODIFY `id` mediumint(8) UNSIGNED NOT NULL AUTO_INCREMENT;

ALTER TABLE `hlstats_Roles`
  MODIFY `roleId` tinyint(3) UNSIGNED NOT NULL AUTO_INCREMENT;

ALTER TABLE `hlstats_Servers`
  MODIFY `serverId` smallint(5) UNSIGNED NOT NULL AUTO_INCREMENT;

ALTER TABLE `hlstats_Teams`
  MODIFY `teamId` tinyint(3) UNSIGNED NOT NULL AUTO_INCREMENT;

ALTER TABLE `hlstats_Weapons`
  MODIFY `weaponId` smallint(5) UNSIGNED NOT NULL AUTO_INCREMENT;

