SELECT pg_advisory_xact_lock(hashtext('machine-whisperer:postgres-exporter'));

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'mw_postgres_exporter') THEN
        CREATE ROLE mw_postgres_exporter;
    END IF;
END
$$;

ALTER ROLE mw_postgres_exporter LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
GRANT pg_monitor TO mw_postgres_exporter;
SET LOCAL password_encryption = 'scram-sha-256';
\password mw_postgres_exporter
