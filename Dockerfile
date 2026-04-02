FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o chart_bot .

FROM alpine:3.21

RUN apk add --no-cache \
    chromium \
    xvfb \
    font-noto-cjk

COPY --from=builder /app/chart_bot /usr/local/bin/chart_bot

ENV CHROME_PATH=/usr/bin/chromium-browser

ENTRYPOINT ["chart_bot"]
