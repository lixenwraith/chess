-- Provision the chess database on PostgreSQL 18. Run once as a superuser:
--
--   psql -X -U postgres -d postgres -f setup.sql                 # owner mode
--   psql -X -U postgres -d postgres -v split=true -f setup.sql   # split mode
--
-- Owner mode (default): the login role `chess` owns schema `chess`, so the
-- server applies migrations itself at startup. It has no other privileges.
--
-- Split mode: NOLOGIN role `chess_owner` owns the schema and tables; the login
-- role `chess` receives only SELECT/INSERT/UPDATE/DELETE. The server starts
-- only when the schema is current; an administrator migrates, as the OS user
-- postgres, with
--   chess-server db init -dsn "dbname=chess user=postgres \
--       options='-c role=chess_owner -c search_path=chess'"
--
-- Neither mode sets a password: the chess OS user authenticates over the Unix
-- socket with peer authentication (see doc/deployment.md for pg_hba.conf).
\set ON_ERROR_STOP on
\if :{?split}
\else
\set split false
\endif

CREATE ROLE chess LOGIN
	NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS
	CONNECTION LIMIT 20;

-- The builtin C.UTF-8 provider gives code-point ordering and Unicode case
-- mapping that do not change with operating-system library upgrades.
CREATE DATABASE chess
	OWNER postgres
	TEMPLATE template0
	ENCODING 'UTF8'
	LOCALE_PROVIDER builtin
	BUILTIN_LOCALE 'C.UTF-8';

REVOKE ALL ON DATABASE chess FROM PUBLIC;
GRANT CONNECT ON DATABASE chess TO chess;

\connect chess

-- Nothing may be created in or read from public; chess lives in its own schema.
REVOKE ALL ON SCHEMA public FROM PUBLIC;

\if :split
CREATE ROLE chess_owner NOLOGIN
	NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE SCHEMA chess AUTHORIZATION chess_owner;
GRANT USAGE ON SCHEMA chess TO chess;
-- Tables created by future migrations inherit runtime DML grants.
ALTER DEFAULT PRIVILEGES FOR ROLE chess_owner IN SCHEMA chess
	GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO chess;
\else
CREATE SCHEMA chess AUTHORIZATION chess;
\endif

-- Resolve unqualified names in the chess schema without a DSN parameter.
ALTER ROLE chess IN DATABASE chess SET search_path = chess;
-- Bound runaway statements, lock waits, and abandoned transactions. The
-- server's own per-operation deadlines are shorter.
ALTER ROLE chess IN DATABASE chess SET statement_timeout = '30s';
ALTER ROLE chess IN DATABASE chess SET lock_timeout = '10s';
ALTER ROLE chess IN DATABASE chess SET idle_in_transaction_session_timeout = '60s';
