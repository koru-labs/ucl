CREATE DATABASE state_db;
CREATE DATABASE pool_db;
CREATE DATABASE event_db;
CREATE DATABASE rpc_db;
CREATE DATABASE prover_db;

\connect event_db;


CREATE TYPE level_t AS ENUM ('emerg', 'alert', 'crit', 'err', 'warning', 'notice', 'info', 'debug');

CREATE TABLE public.event (
                              id BIGSERIAL PRIMARY KEY,
                              received_at timestamp WITH TIME ZONE default CURRENT_TIMESTAMP,
                              ip_address inet,
                              source varchar(32) not null,
                              component varchar(32),
                              level level_t not null,
                              event_id varchar(32) not null,
                              description text,
                              data bytea,
                              json jsonb
);


\connect prover_db;
CREATE SCHEMA state;

CREATE TABLE state.nodes (hash BYTEA PRIMARY KEY, data BYTEA NOT NULL);
CREATE TABLE state.program (hash BYTEA PRIMARY KEY, data BYTEA NOT NULL);

CREATE USER prover_user with password 'prover_pass';
ALTER DATABASE prover_db OWNER TO prover_user;
ALTER SCHEMA state OWNER TO prover_user;
ALTER SCHEMA public OWNER TO prover_user;
ALTER TABLE state.nodes OWNER TO prover_user;
ALTER TABLE state.program OWNER TO prover_user;
ALTER USER prover_user SET SEARCH_PATH=state;
