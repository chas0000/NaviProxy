# 构建阶段 - 复制文件
FROM alpine:latest AS builder

# 安装运行时依赖（SQLite库）
RUN apk add --no-cache ca-certificates tzdata sqlite-libs

# 设置工作目录
WORKDIR /app

# 复制预编译的二进制文件（固定名称）
COPY naviproxy-binary /app/naviproxy

# 复制配置示例
COPY data/config.example.yaml /app/data/config.example.yaml

# 运行阶段
FROM alpine:latest

# 安装运行时依赖（SQLite库）
RUN apk add --no-cache ca-certificates tzdata sqlite-libs

# 设置时区
ENV TZ=Asia/Shanghai

# 设置工作目录
WORKDIR /app

# 复制编译好的二进制文件
COPY --from=builder /app/naviproxy /app/naviproxy
COPY --from=builder /app/data/config.example.yaml /app/data/config.example.yaml

# 创建数据目录
RUN mkdir -p /app/data

# 暴露端口
EXPOSE 8094

# 运行程序
CMD ["/app/naviproxy"]