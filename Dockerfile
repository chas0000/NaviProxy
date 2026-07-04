# 构建阶段 - 选择对应架构的二进制文件
FROM alpine:latest AS builder

# 接收构建参数
ARG BINARY_AMD64
ARG BINARY_ARM64

# 根据目标架构复制对应的二进制文件
ARG TARGETARCH
ARG TARGETPLATFORM

# 显示调试信息
RUN echo "TARGETARCH=${TARGETARCH}" && echo "TARGETPLATFORM=${TARGETPLATFORM}"

# 根据架构复制对应的二进制文件
RUN if [ "${TARGETARCH}" = "amd64" ]; then \
      echo "Copying AMD64 binary: ${BINARY_AMD64}"; \
      cp ${BINARY_AMD64} /app/naviproxy; \
    elif [ "${TARGETARCH}" = "arm64" ]; then \
      echo "Copying ARM64 binary: ${BINARY_ARM64}"; \
      cp ${BINARY_ARM64} /app/naviproxy; \
    else \
      echo "Unknown architecture: ${TARGETARCH}"; \
      exit 1; \
    fi

# 检查二进制文件是否存在
RUN ls -la /app/ && file /app/naviproxy

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