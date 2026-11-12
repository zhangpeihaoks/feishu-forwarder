# 构建阶段：Go 1.27（go.mod 要求 >= 1.26.2）
# 基础镜像前缀走国内可达的镜像站：NAS/构建机直连 registry-1.docker.io 会超时
FROM docker.m.daocloud.io/library/golang:1.27-alpine AS build

WORKDIR /src

# 依赖层：先拷 go.mod/go.sum，源码改动不会击穿模块下载缓存。
# 本服务只依赖 firefly（公开仓），不需要私有模块凭据。
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# 源码层：VERSION / COMMIT 由发布工作流注入，`-version` 与日志里可核对线上版本
COPY . .
ARG VERSION=dev
ARG COMMIT=none
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /out/feishu-forwarder .

# 运行阶段：alpine 自带的 busybox wget 供 k8s exec 探针使用；
# ca-certificates 必须装，否则到 open.feishu.cn 的 HTTPS 会报证书错误
FROM docker.m.daocloud.io/library/alpine:3.22

# 时区数据只在本地排障时有用，体积很小
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 appuser

WORKDIR /app
COPY --from=build /out/feishu-forwarder /app/feishu-forwarder

# 非 root 运行；根文件系统在清单里是只读的，本镜像也不需要写任何文件
USER 10001

EXPOSE 8080
ENTRYPOINT ["/app/feishu-forwarder"]
