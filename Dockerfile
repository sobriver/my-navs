# 构建阶段在构建机的原生架构上运行，通过 GOARCH 交叉编译，不需要 QEMU 模拟
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY web ./web
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/my-navs . \
 && mkdir -p /out/data

# 运行阶段：空镜像，只有一个静态二进制和 CA 证书（下载图标时校验 HTTPS 用）
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/my-navs /my-navs
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
ENV DATA_DIR=/data \
    PORT=8080 \
    GOMEMLIMIT=40MiB
EXPOSE 8080
VOLUME /data
HEALTHCHECK --interval=60s --timeout=5s --start-period=5s --retries=3 CMD ["/my-navs", "healthcheck"]
ENTRYPOINT ["/my-navs"]
