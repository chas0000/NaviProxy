# 构建阶段
FROM alpine:latest AS builder

# 接收构建参数
ARG BINARY_AMD64
ARG BINARY_ARM64

# 显示调试信息
RUN echo "BINARY_AMD64=${BINARY_AMD64}" && echo "BINARY_ARM64=${BINARY_ARM64}"

# 复制二进制文件到临时位置
COPY ${BINARY_AMD64} /app/naviproxy-amd64
COPY ${BINARY_ARM64} /app/naviproxy-arm64

# 显示复制的文件
RUN ls -la /app/

# 根据目标架构选择对应的二进制文件
ARG TARGETARCH
RUN if [ "${TARGETARCH}" = "amd64" ]; then \
      echo "Selecting AMD64 binary"; \
      cp /app/naviproxy-amd64 /app/naviproxy; \
    elif [ "${TARGETARCH}" = "arm64" ]; then \
      echo "Selecting ARM64 binary"; \
      cp /app/naviproxy-arm64 /app/naviproxy; \
    else \
      echo "Unknown architecture: ${TARGETARCH}"; \
      ls -la /app/; \
      exit 1; \
    fi

# 检查最终二进制文件
RUN ls -la /app/

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

# 设置执行权限
RUN chmod +x /app/naviproxy && ls -la /app/

# 创建数据目录
RUN mkdir -p /app/data

# 暴露端口
EXPOSE 8094

# 运行程序
CMD ["/app/naviproxy"]