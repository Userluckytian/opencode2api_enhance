# 网页自助领 Key 服务（web/）多阶段构建
# 纯 Go 标准库，零外部依赖，交叉编译为静态二进制，运行时镜像仅 ~15MB
FROM golang:1.22-alpine AS builder

WORKDIR /src
COPY go.mod ./
# web/ 及其测试不依赖任何外部包，直接拷贝源码
COPY web/ ./web/

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/web-server ./web

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 webuser

WORKDIR /app
COPY --from=builder /out/web-server /app/web-server

# 数据目录（config.json / keys.json / logs/）挂载为卷
VOLUME ["/app/web-data"]
EXPOSE 23081

USER webuser
ENTRYPOINT ["/app/web-server"]
CMD ["-data-dir", "/app/web-data"]
