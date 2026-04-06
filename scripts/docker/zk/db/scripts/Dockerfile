FROM postgres:15

COPY single_db_server.sql /docker-entrypoint-initdb.d/

ENV POSTGRES_USER=hamsa-dev
ENV POSTGRES_PASSWORD=dev123
ENV POSTGRES_DB=test