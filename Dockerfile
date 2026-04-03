FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o chart_bot .

FROM alpine:3.21

RUN apk add --no-cache \
    chromium \
    font-noto-cjk \
    && rm -rf /usr/share/doc /usr/share/man

COPY --from=builder /app/chart_bot /usr/local/bin/chart_bot

ENV CHROME_PATH=/usr/bin/chromium-browser

CMD ["chart_bot"]
