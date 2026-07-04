# 构建阶段
FROM golang:1.21-alpine AS builder

# 安装构建依赖
RUN apk add --no-cache git gcc musl-dev sqlite-dev

# 设置工作目录
WORKDIR /build

# 复制依赖文件
COPY go.mod go.sum ./
RUN go mod download

# 复制源代码
COPY . .

# 编译二进制文件（使用优化选项减小体积）
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -trimpath \
    -o navidrome-proxy main.go

# 运行阶段 - 使用最小化的 alpine 镜像
FROM alpine:latest

# 安装运行时依赖（SQLite库）
RUN apk add --no-cache ca-certificates tzdata sqlite-libs

# 设置时区
ENV TZ=Asia/Shanghai

# 设置工作目录
WORKDIR /app

# 复制编译好的二进制文件
COPY --from=builder /build/navidrome-proxy /app/

# 复制配置示例
COPY --from=builder /build/data/config.example.yaml /app/data/config.example.yaml

# 创建数据目录
RUN mkdir -p /app/data

# 暴露端口
EXPOSE 8094

# 运行程序
CMD ["/app/navidrome-proxy"]