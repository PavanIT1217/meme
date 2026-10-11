FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /meme-server ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app && mkdir /data && chown app /data
USER app
COPY --from=build /meme-server /usr/local/bin/meme-server
ENV DB_PATH=/data/meme.db UPLOAD_DIR=/data/uploads PORT=8080
VOLUME /data
EXPOSE 8080
CMD ["meme-server"]
