-- Existing numbers and UUID identities remain unchanged. Application code now
-- allocates cryptographically random six-digit numbers in the creation transaction.
DROP TRIGGER profile_allocate;
DROP TRIGGER immutable_account_number;
DROP TABLE account_allocator;

CREATE TRIGGER account_number_format BEFORE UPDATE OF account_number ON profiles
WHEN NEW.account_number IS NULL OR length(NEW.account_number) NOT BETWEEN 6 AND 10
 OR NEW.account_number GLOB '*[^0-9]*' OR substr(NEW.account_number,1,1)='0'
BEGIN SELECT RAISE(ABORT,'invalid account number'); END;

CREATE TRIGGER account_number_login_collision BEFORE UPDATE OF account_number ON profiles
WHEN EXISTS(SELECT 1 FROM users WHERE id<>NEW.user_id AND (username=NEW.account_number OR email=NEW.account_number))
BEGIN SELECT RAISE(ABORT,'account number collides with existing login'); END;

CREATE TRIGGER email_number_collision_insert BEFORE INSERT ON users
WHEN NEW.email<>'' AND EXISTS(SELECT 1 FROM profiles WHERE account_number=NEW.email)
BEGIN SELECT RAISE(ABORT,'email collides with account number'); END;

CREATE TRIGGER email_number_collision_update BEFORE UPDATE OF email ON users
WHEN NEW.email<>'' AND EXISTS(SELECT 1 FROM profiles WHERE account_number=NEW.email AND user_id<>NEW.id)
BEGIN SELECT RAISE(ABORT,'email collides with account number'); END;
