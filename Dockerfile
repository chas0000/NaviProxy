# 运行阶段
FROM debian:stable-slim

# 安装运行时依赖（SQLite库和CA证书）
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    tzdata \
    libsqlite3-0 \
    && rm -rf /var/lib/apt/lists/*

# 设置时区
ENV TZ=Asia/Shanghai

# 设置工作目录
WORKDIR /app

# 根据目标架构选择对应的二进制文件
ARG TARGETARCH
RUN echo "Target architecture: ${TARGETARCH}"

# 复制两个架构的二进制文件到临时目录
COPY naviproxy-binary-amd64 /tmp/naviproxy-amd64
COPY naviproxy-binary-arm64 /tmp/naviproxy-arm64

# 根据目标架构选择并复制正确的二进制文件
RUN if [ "${TARGETARCH}" = "amd64" ]; then \
      echo "Selecting AMD64 binary"; \
      cp /tmp/naviproxy-amd64 /app/naviproxy; \
    elif [ "${TARGETARCH}" = "arm64" ]; then \
      echo "Selecting ARM64 binary"; \
      cp /tmp/naviproxy-arm64 /app/naviproxy; \
    else \
      echo "Unknown architecture: ${TARGETARCH}"; \
      exit 1; \
    fi && \
    chmod +x /app/naviproxy && \
    ls -la /app/naviproxy && \
    echo "Binary check: $(test -x /app/naviproxy && echo EXECUTABLE || echo NOT_EXECUTABLE)"

# 复制配置示例
COPY data/config.example.yaml /app/data/config.example.yaml

# 创建数据目录
RUN mkdir -p /app/data

# 暴露端口
EXPOSE 8094

# 运行程序
CMD ["/app/naviproxy"]
