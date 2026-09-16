// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	pgx "github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// An instance's relational database, and the login that owns it.
//
// 🔑 THE ISOLATION IS OWNERSHIP, NOT CONVENTION. Every instance on a cluster shares one
// relational store. What keeps one instance out of another's data is that each
// instance's services connect as a login of their own, which owns exactly one database
// — the one named after the instance — and that PUBLIC has had CONNECT on it revoked.
// No login a service holds can create a database or a role, so nothing running in a
// pod can widen that.
//
// The one identity that can is the cluster's provisioner role, and only dcctl signs in
// as it. Everything below runs as that role.
//
// 🔴 POSTGRESQL 16 CHANGED WHAT CREATEROLE MEANS, AND THE STATEMENTS FOLLOW FROM IT. A
// CREATEROLE role that creates a role is given ADMIN on it, and nothing else: it cannot
// act as that role, and ownership checks do not pass through it. So `CREATE DATABASE …
// OWNER x` — which requires being able to SET ROLE x — fails for the role that has just
// created x, until it grants itself SET on x. That grant is deliberately SET without
// INHERIT: the provisioner can become an instance's login to create, revoke on and drop
// that instance's database, and holds none of its privileges otherwise.

// instanceDBQuerier is the part of a pgx connection these statements need. One
// SESSION, not a pool: `SET ROLE` is session state, and a pool would run the
// statement after it on a connection that never heard of it.
type instanceDBQuerier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconnCommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// pgconnCommandTag keeps the interface satisfiable by *pgx.Conn without importing
// pgconn here for one type.
type pgconnCommandTag = interface{ String() string }

// pgxSession adapts *pgx.Conn to instanceDBQuerier.
type pgxSession struct{ conn *pgx.Conn }

func (s pgxSession) Exec(ctx context.Context, sql string, args ...any) (pgconnCommandTag, error) {
	return s.conn.Exec(ctx, sql, args...)
}

func (s pgxSession) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return s.conn.QueryRow(ctx, sql, args...)
}

// errInstanceDatabaseNotOurs marks a refusal: something by this name exists and is not
// what dcctl would have made. A caller retrying on transient errors must not retry this.
var errInstanceDatabaseNotOurs = errors.New("the instance's database or login is not dcctl's")

// ensureInstanceDatabase makes the instance's login and database exist, owned and
// fenced as described above, with the login holding password. Safe to run again: a
// re-run changes only the password, to the one given.
func ensureInstanceDatabase(ctx context.Context, q instanceDBQuerier, instance, password string) error {
	if err := ValidateInstanceName(instance); err != nil {
		return err
	}
	ident := pgx.Identifier{instance}.Sanitize()
	verifier, err := scramSHA256Verifier(password)
	if err != nil {
		return err
	}

	// THE LOGIN.
	var exists, super, createdb, createrole, admin bool
	err = q.QueryRow(ctx, `
		select true, r.rolsuper, r.rolcreatedb, r.rolcreaterole,
		       coalesce((select bool_or(m.admin_option) from pg_auth_members m
		                 where m.roleid = r.oid and m.member = (select oid from pg_roles where rolname = current_user)), false)
		from pg_roles r where r.rolname = $1`, instance).Scan(&exists, &super, &createdb, &createrole, &admin)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// 🔴 THE PASSWORD TRAVELS AS A SCRAM VERIFIER, NEVER AS ITSELF. A role's
		// password cannot be a bind parameter — CREATE ROLE is a utility statement — so
		// whatever is written here is statement text, which is exactly what
		// log_statement and pg_stat_statements record. A verifier is what the server
		// would have stored anyway, and it cannot be replayed as a login.
		if _, err := q.Exec(ctx, fmt.Sprintf(
			"CREATE ROLE %s LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION PASSWORD '%s'",
			ident, verifier)); err != nil {
			return fmt.Errorf("creating the login for instance %q: %w", instance, err)
		}
	case err != nil:
		return fmt.Errorf("looking up the login for instance %q: %w", instance, err)
	default:
		// 🔴 A ROLE BY THIS NAME THAT THE PROVISIONER DID NOT CREATE IS NOT REUSED. ADMIN
		// is what creating it granted, so its absence means somebody else made it — and
		// re-passwording a stranger's role would hand this instance's services whatever
		// that role can reach.
		if !admin {
			return fmt.Errorf("%w: a role named %q already exists on the relational store and was "+
				"not created by dcctl's provisioner, so it may be something else's login. Refusing to "+
				"take it over; remove it, or choose another instance name", errInstanceDatabaseNotOurs, instance)
		}
		if super || createdb || createrole {
			return fmt.Errorf("%w: the login %q holds SUPERUSER, CREATEDB or CREATEROLE, which an "+
				"instance's login never has — with any of them it could reach past its own database. "+
				"Refusing to hand it to services", errInstanceDatabaseNotOurs, instance)
		}
		if _, err := q.Exec(ctx, fmt.Sprintf("ALTER ROLE %s PASSWORD '%s'", ident, verifier)); err != nil {
			return fmt.Errorf("setting the password of the login for instance %q: %w", instance, err)
		}
	}

	// SET, so the provisioner can act as the login; INHERIT FALSE, so it holds none of
	// the login's privileges while it is not.
	if _, err := q.Exec(ctx, fmt.Sprintf("GRANT %s TO CURRENT_USER WITH SET TRUE, INHERIT FALSE", ident)); err != nil {
		return fmt.Errorf("letting the provisioner act as the login for instance %q: %w", instance, err)
	}

	// THE DATABASE.
	var owner string
	err = q.QueryRow(ctx,
		`select pg_get_userbyid(datdba) from pg_database where datname = $1`, instance).Scan(&owner)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// CREATE DATABASE cannot run inside a transaction block, and does not here: each
		// Exec on a plain connection is its own implicit transaction.
		if _, err := q.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s OWNER %s", ident, ident)); err != nil {
			return fmt.Errorf("creating the database for instance %q: %w", instance, err)
		}
	case err != nil:
		return fmt.Errorf("looking up the database for instance %q: %w", instance, err)
	case owner != instance:
		// 🔴 THE PRE-ISOLATION SHAPE. Before instances had logins of their own, every
		// service created this database as the store's shared owner. Its tables belong
		// to that owner, so handing the instance its own login would leave services unable
		// to read their own data — and quietly re-owning it would carry forward exactly
		// the access this change removes.
		return fmt.Errorf("%w: database %q already exists on the relational store and is owned by %q, "+
			"not by the instance's own login. It was created before each instance had a login of its "+
			"own, or it belongs to something else. Recreate the instance (`dcctl destroy` then "+
			"`dcctl bootstrap`) on a cluster built by this dcctl", errInstanceDatabaseNotOurs, instance, owner)
	}

	// 🔴 AS THE OWNER. REVOKE by anyone else who holds no grant option is not an error —
	// it is a WARNING that nothing was revoked, and PUBLIC keeps CONNECT. So the revoke
	// runs as the login, and the result is then read back rather than assumed.
	if _, err := q.Exec(ctx, "SET ROLE "+ident); err != nil {
		return fmt.Errorf("acting as the login for instance %q: %w", instance, err)
	}
	_, revokeErr := q.Exec(ctx, fmt.Sprintf("REVOKE CONNECT, TEMPORARY ON DATABASE %s FROM PUBLIC", ident))
	if _, err := q.Exec(ctx, "RESET ROLE"); err != nil {
		return fmt.Errorf("returning to the provisioner after acting as %q: %w", instance, err)
	}
	if revokeErr != nil {
		return fmt.Errorf("revoking PUBLIC's access to the database for instance %q: %w", instance, revokeErr)
	}
	var publicCanConnect bool
	if err := q.QueryRow(ctx,
		`select has_database_privilege('public', $1, 'CONNECT')`, instance).Scan(&publicCanConnect); err != nil {
		return fmt.Errorf("checking PUBLIC's access to the database for instance %q: %w", instance, err)
	}
	if publicCanConnect {
		return fmt.Errorf("database %q is still open to every login on the relational store after "+
			"revoking PUBLIC's access, so any other instance could connect to it", instance)
	}
	return nil
}

// dropInstanceDatabase removes the instance's database and login, and reports success
// only once the catalog shows neither. Absent already is success.
func dropInstanceDatabase(ctx context.Context, q instanceDBQuerier, instance string) error {
	if err := ValidateInstanceName(instance); err != nil {
		return err
	}
	ident := pgx.Identifier{instance}.Sanitize()

	var owner string
	err := q.QueryRow(ctx,
		`select pg_get_userbyid(datdba) from pg_database where datname = $1`, instance).Scan(&owner)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return fmt.Errorf("looking up the database for instance %q: %w", instance, err)
	case owner != instance:
		return fmt.Errorf("%w: database %q is owned by %q, not by the instance's own login, so dcctl did "+
			"not create it and will not drop it", errInstanceDatabaseNotOurs, instance, owner)
	default:
		// As the owner, for the reason ensureInstanceDatabase revokes as the owner:
		// ownership does not pass through a membership without INHERIT. FORCE ends the
		// sessions still open on it, which are all this instance's.
		if _, err := q.Exec(ctx, "SET ROLE "+ident); err != nil {
			return fmt.Errorf("acting as the login for instance %q: %w", instance, err)
		}
		_, dropErr := q.Exec(ctx, fmt.Sprintf("DROP DATABASE %s WITH (FORCE)", ident))
		if _, err := q.Exec(ctx, "RESET ROLE"); err != nil {
			return fmt.Errorf("returning to the provisioner after acting as %q: %w", instance, err)
		}
		if dropErr != nil {
			// 🔴 A SESSION THIS ROLE CANNOT END IS A WAIT, NOT A REFUSAL. FORCE terminates
			// the database's other sessions, but a non-superuser may not terminate a
			// superuser's — and the database operator's metrics exporter connects to every
			// database as one, briefly, on each scrape. So "permission denied to terminate"
			// and "being accessed by other users" are retried until the exporter lets go.
			if code := pgErrorCode(dropErr); code == "42501" || code == "55006" {
				return notReady("dropping the database for instance %q while a session holds it: %v", instance, dropErr)
			}
			return fmt.Errorf("dropping the database for instance %q: %w", instance, dropErr)
		}
	}

	var admin bool
	err = q.QueryRow(ctx, `
		select coalesce((select bool_or(m.admin_option) from pg_auth_members m
		                 where m.roleid = r.oid and m.member = (select oid from pg_roles where rolname = current_user)), false)
		from pg_roles r where r.rolname = $1`, instance).Scan(&admin)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return fmt.Errorf("looking up the login for instance %q: %w", instance, err)
	case !admin:
		return fmt.Errorf("%w: role %q was not created by dcctl's provisioner, so it will not be dropped. "+
			"If it is this instance's login and nothing else uses it, a database superuser can remove it "+
			"(`DROP ROLE %s`) and the destroy can be run again", errInstanceDatabaseNotOurs, instance, ident)
	default:
		if _, err := q.Exec(ctx, "DROP ROLE "+ident); err != nil {
			return fmt.Errorf("dropping the login for instance %q: %w", instance, err)
		}
	}

	// 🔴 VERIFIED, NOT ASSUMED. The statements above can succeed and still leave something
	// behind — a database recreated by a service mid-drop, a role another grantor still
	// holds — and a destroy that reports success over a database still sitting in the
	// shared store is the one outcome this function exists to prevent.
	var dbLeft, roleLeft bool
	if err := q.QueryRow(ctx, `select exists(select 1 from pg_database where datname = $1),
	                                  exists(select 1 from pg_roles where rolname = $1)`,
		instance).Scan(&dbLeft, &roleLeft); err != nil {
		return fmt.Errorf("confirming the database and login for instance %q are gone: %w", instance, err)
	}
	if dbLeft || roleLeft {
		return fmt.Errorf("after dropping them, the relational store still has database=%t login=%t "+
			"for instance %q", dbLeft, roleLeft, instance)
	}
	return nil
}

// pgErrorCode is the SQLSTATE of a PostgreSQL error, or "" for anything else.
func pgErrorCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// scramSHA256Verifier computes the SCRAM-SHA-256 verifier PostgreSQL stores for a
// password, in the form it accepts as `PASSWORD '<verifier>'`.
//
// Restricted to printable ASCII, where SASLprep — which the server applies to the
// password at login — is the identity. Every password dcctl mints is URL-safe base64;
// anything outside that range is refused rather than hashed into a verifier the login
// would never match.
func scramSHA256Verifier(password string) (string, error) {
	if password == "" {
		return "", errors.New("refusing to set an empty password on an instance's login")
	}
	for _, r := range password {
		if r < 0x21 || r > 0x7e {
			return "", errors.New("an instance login's password must be printable ASCII")
		}
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("reading randomness for a password salt: %w", err)
	}
	const iterations = 4096
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return "", err
	}
	mac := func(key []byte, msg string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(msg))
		return h.Sum(nil)
	}
	stored := sha256.Sum256(mac(salted, "Client Key"))
	server := mac(salted, "Server Key")
	b64 := base64.StdEncoding.EncodeToString
	v := fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", iterations, b64(salt), b64(stored[:]), b64(server))
	if strings.ContainsRune(v, '\'') {
		return "", errors.New("a SCRAM verifier contained a quote, which base64 cannot produce")
	}
	return v, nil
}
