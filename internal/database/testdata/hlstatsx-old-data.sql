-- Test fixture: a few synthetic rows for testdata/hlstatsx-old.sql (MariaDB).
-- Addresses are documentation addresses (RFC 5737), the Steam IDs are
-- made up. The upgrade test checks that gamewe db migrate keeps every row.

INSERT INTO hlstats_Games (id, code, name, hidden) VALUES
  (1, 'css', 'Counter-Strike: Source', '0'),
  (2, 'css2', 'CS:S Clone', '1');
INSERT INTO hlstats_Servers (serverId, address, port, name, game, kills) VALUES
  (1, '192.0.2.10', 27015, 'Test server 1', 'css', 3),
  (2, '198.51.100.20', 27016, 'Test server 2', 'css2', 0);
INSERT INTO hlstats_Players (playerId, game, game_id, skill, kills, deaths, last_event) VALUES
  (1, 'css', 1, 1010, 2, 1, 1761300000),
  (2, 'css', 1, 990, 1, 2, 1761300060),
  (3, 'css2', 2, 1000, 0, 0, 1761300120);
INSERT INTO hlstats_PlayerUniqueIds (playerId, uniqueId, game) VALUES
  (1, '0:1:1001', 'css'), (2, '0:0:1002', 'css'), (3, '0:1:1003', 'css2');
INSERT INTO hlstats_PlayerNames (playerId, name, connection_time, numuses, kills, deaths) VALUES
  (1, 'Alpha', 600, 1, 2, 1), (2, 'Bravo ä', 600, 1, 1, 2), (3, 'Charlie', 60, 1, 0, 0);
INSERT INTO hlstats_Events_Frags (timestamp_id, game_id, map, killerId, victimId, weapon, headshot) VALUES
  (1761300000, 1, 1, 1, 2, 1, 1),
  (1761300030, 1, 1, 2, 1, 1, 0),
  (1761300060, 1, 1, 1, 2, 2, 0);
INSERT INTO hlstats_Options (keyname, value) VALUES ('sitename', 'Old stats'), ('rankingtype', 'skill');
INSERT INTO hlstats_Users (username, password, acclevel, playerId) VALUES ('admin', 'not-a-real-hash', 100, 0);
