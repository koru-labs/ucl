# 使用官方的 PostgreSQL 镜像作为基础镜像
FROM postgres:15

# 复制初始化脚本到镜像中
COPY single_db_server.sql /docker-entrypoint-initdb.d/

# 设置环境变量
ENV POSTGRES_USER=hamsa-dev
ENV POSTGRES_PASSWORD=dev123
ENV POSTGRES_DB=test