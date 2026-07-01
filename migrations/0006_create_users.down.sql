-- 0006_create_users.down.sql
-- Reverses the up migration. sessions (0007) FK users(id), so run 0007's down
-- first -- this DROP fails while a session row still references a user.
DROP TABLE IF EXISTS users;
