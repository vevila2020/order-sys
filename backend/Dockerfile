# 多阶段构建：API 与 worker 是同一份代码的不同入口。
#
# 通过 TARGET 构建参数决定编译哪个 cmd，避免一个镜像里塞入两个二进制。
# 运行阶段使用固定版本 alpine，保留 wget 供 Compose 健康检查，并以非 root 运行。

# ---------- build ----------
# 固定到具体补丁版本：golang:1.26-alpine 当前解析到 1.26.9，但标签本身会漂移。
# 这里必须 >= go.mod 的 go 指令，否则镜像内 go mod download 会直接失败。
FROM golang:1.26.9-alpine AS build

# git 用于 go mod 下载私有仓库；ca-certificates 供构建期访问 HTTPS。
RUN apk add --no-cache git ca-certificates

WORKDIR /src

# GOPROXY 可通过构建参数覆盖：CI 与外网环境使用默认官方代理，
# 内网或官方代理不可达时改用镜像（例如 GOPROXY=https://goproxy.cn,direct）。
# 默认值保持官方配置，避免把某个第三方镜像固化成项目共识。
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}

# 先复制依赖清单并下载，充分利用 Docker 层缓存。
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGET=api
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

# CGO_ENABLED=0 生成静态二进制，便于在最小镜像中运行。
# -trimpath 去除本机路径，保证构建可复现。
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w \
      -X github.com/vevila2020/order-sys/internal/version.Version=${VERSION} \
      -X github.com/vevila2020/order-sys/internal/version.Commit=${COMMIT} \
      -X github.com/vevila2020/order-sys/internal/version.BuildDate=${BUILD_DATE}" \
    -o /out/app ./cmd/${TARGET}

# ---------- runtime ----------
FROM alpine:3.21

# wget 供容器健康检查；tzdata 保证日志时间可预测（业务时间统一 UTC）。
# 先 upgrade 再安装：基础镜像里的 openssl 等系统库可能落后于 alpine 仓库的补丁版本，
# 不升级会让 Trivy 持续报 HIGH/CRITICAL（CI 的 docker job 会因此失败）。
RUN apk upgrade --no-cache \
    && apk add --no-cache wget tzdata \
    && addgroup -S app \
    && adduser -S -G app -h /app app

WORKDIR /app
COPY --from=build /out/app /app/app

# 非 root 运行，且不携带任何配置文件或密钥。
USER app
EXPOSE 8080 8081

ENTRYPOINT ["/app/app"]
