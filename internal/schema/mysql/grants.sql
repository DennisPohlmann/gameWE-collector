-- gameWE - collector: table rights of the web users.
--
-- Compiled into the collector (internal/schema): `gamewe db migrate` runs
-- it after it created or changed tables (MySQL / MariaDB only grant table
-- and column rights on tables that exist), as the collector user, which
-- needs WITH GRANT OPTION for it (schema/init/00_create_database.sql).
-- `gamewe db grants -print` prints it for running as root instead. The users
-- are created in 00_create_database.sql. Table names are unqualified, so the
-- rights apply to the database the script runs in.
--
-- gamewe_web (public web pages): SELECT on every table, except
-- hlstats_Users (web admin logins), hlstats_Events_Rcon (RCON commands
-- and passwords) and hlstats_Events_Chat (chat, personal data), and without hlstats_Servers.rcon_password,
-- log_secret and log_secret_changed and hlstats_Events_Connects.ipAddress
-- (column rights). If a table is added to
-- the schema, grant it here too (a test in internal/database checks this file).
-- MariaDB keeps table rights when a database is dropped: on a database
-- created again under the same name the web user would get old rights back.
DELIMITER //
BEGIN NOT ATOMIC
  DECLARE CONTINUE HANDLER FOR 1141, 1147 BEGIN END; -- no such grant
  REVOKE SELECT ON `hlstats_Users` FROM 'gamewe_web'@'%';
  REVOKE SELECT ON `hlstats_Events_Rcon` FROM 'gamewe_web'@'%';
  REVOKE SELECT ON `hlstats_Events_Chat` FROM 'gamewe_web'@'%';
  REVOKE SELECT ON `hlstats_Events_Connects` FROM 'gamewe_web'@'%';
  REVOKE SELECT (`ipAddress`) ON `hlstats_Events_Connects` FROM 'gamewe_web'@'%';
END //
DELIMITER ;
GRANT SELECT ON `hlstats_Actions` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Awards` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Awards_History` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Bans` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Bans_Players` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_ClanTags` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Clans` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Definition_Ranks_Ribbons` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Definition_Ribbons_Classes` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Events_Admin` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Events_ChangeName` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Events_ChangeRole` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Events_ChangeTeam` TO 'gamewe_web'@'%';
GRANT SELECT (`timestamp_id`, `game_id`, `map`, `playerId`, `hostname`, `hostgroup`) ON `hlstats_Events_Connects` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Events_Disconnects` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Events_Entries` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Events_Frags` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Events_Latency` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Events_Locations` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Events_PlayerActions` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Events_PlayerPlayerActions` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Events_Suicides` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Events_TeamBonuses` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Events_Teamkills` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Games` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_gamewe_hall_of_fame` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_gamewe_jobs` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_HostGroups` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Livestats` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Maps` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Options` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_PlayerNames` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_PlayerUniqueIds` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Players` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_PlayersInactive` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Players_Actions` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Players_Actions_History_Months` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Players_Bans` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Players_Data` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Players_History` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Players_Locations` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Players_Maps` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Players_Maps_History_Months` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Players_Resets` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Players_Roles` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Players_Roles_History_Months` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Players_Teams` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Players_Teams_History_Months` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Players_Weapons` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Players_Weapons_History_Months` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Roles` TO 'gamewe_web'@'%';
GRANT SELECT (`serverId`, `address`, `port`, `name`, `game`, `publicaddress`, `statusurl`, `kills`, `players`, `rounds`, `suicides`, `headshots`, `bombs_planted`, `bombs_defused`, `ct_wins`, `ts_wins`, `act_players`, `max_players`, `act_map`, `map_rounds`, `map_ct_wins`, `map_ts_wins`, `map_started`, `map_changes`, `ct_shots`, `ct_hits`, `ts_shots`, `ts_hits`, `map_ct_shots`, `map_ct_hits`, `map_ts_shots`, `map_ts_hits`, `display_id`, `lat`, `lng`, `country_code`, `country_name`, `city`, `state`, `fixed_location`, `account_id`, `vac_status`, `steam_status`, `server_version`, `server_os`, `server_vac`, `act_bots`) ON `hlstats_Servers` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Style` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Teams` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Trend` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_Weapons` TO 'gamewe_web'@'%';
GRANT SELECT ON `hlstats_server_load` TO 'gamewe_web'@'%';

-- gamewe_web_admin (web admin pages): reads everything (granted on the
-- database in 00), edits the settings and reference tables, and on players
-- only skill, hideranking and clan. It cannot write events or statistics.
GRANT INSERT, UPDATE, DELETE ON `hlstats_Options` TO 'gamewe_web_admin'@'%';
GRANT INSERT, UPDATE, DELETE ON `hlstats_Games` TO 'gamewe_web_admin'@'%';
GRANT INSERT, UPDATE, DELETE ON `hlstats_Servers` TO 'gamewe_web_admin'@'%';
GRANT INSERT, UPDATE, DELETE ON `hlstats_Clans` TO 'gamewe_web_admin'@'%';
GRANT INSERT, UPDATE, DELETE ON `hlstats_Weapons` TO 'gamewe_web_admin'@'%';
GRANT INSERT, UPDATE, DELETE ON `hlstats_Actions` TO 'gamewe_web_admin'@'%';
GRANT INSERT, UPDATE, DELETE ON `hlstats_Teams` TO 'gamewe_web_admin'@'%';
GRANT INSERT, UPDATE, DELETE ON `hlstats_Roles` TO 'gamewe_web_admin'@'%';
GRANT INSERT, UPDATE, DELETE ON `hlstats_Awards` TO 'gamewe_web_admin'@'%';
GRANT INSERT, UPDATE, DELETE ON `hlstats_Definition_Ranks_Ribbons` TO 'gamewe_web_admin'@'%';
GRANT INSERT, UPDATE, DELETE ON `hlstats_Users` TO 'gamewe_web_admin'@'%';
GRANT UPDATE (`skill`, `hideranking`, `clan`) ON `hlstats_Players` TO 'gamewe_web_admin'@'%';
-- ranking resets: queued for the collector, which does and updates them
GRANT INSERT ON `hlstats_gamewe_jobs` TO 'gamewe_web_admin'@'%';
-- hall of fame: an admin removes (and restores) entries, the rows stay
GRANT UPDATE (`removed`) ON `hlstats_gamewe_hall_of_fame` TO 'gamewe_web_admin'@'%';
-- A user gamewe_admin (the manual setup in the web frontend's README) may
-- queue ranking resets too, for every host it is defined for; skipped when
-- there is none.
DELIMITER //
BEGIN NOT ATOMIC
  DECLARE done INT DEFAULT 0;
  DECLARE h VARCHAR(255);
  DECLARE c CURSOR FOR SELECT Host FROM mysql.user WHERE User = 'gamewe_admin';
  DECLARE CONTINUE HANDLER FOR NOT FOUND SET done = 1;
  OPEN c;
  l: LOOP
    FETCH c INTO h;
    IF done THEN LEAVE l; END IF;
    EXECUTE IMMEDIATE CONCAT('GRANT SELECT, INSERT ON `hlstats_gamewe_jobs` TO ''gamewe_admin''@', QUOTE(h));
  END LOOP;
  CLOSE c;
END //
DELIMITER ;
