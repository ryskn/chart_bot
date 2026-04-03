FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o chart_bot .

FROM ubuntu:24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    gnupg \
    xvfb \
    fonts-noto-cjk \
    fontconfig \
    ca-certificates \
    && curl -fsSLo /usr/share/keyrings/brave-browser-archive-keyring.gpg \
       https://brave-browser-apt-release.s3.brave.com/brave-browser-archive-keyring.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/brave-browser-archive-keyring.gpg] https://brave-browser-apt-release.s3.brave.com/ stable main" \
       > /etc/apt/sources.list.d/brave-browser-release.list \
    && apt-get update && apt-get install -y --no-install-recommends brave-browser \
    && apt-get purge -y curl gnupg && apt-get autoremove -y \
    && rm -rf /var/lib/apt/lists/*

# M+ フォント
RUN apt-get update && apt-get install -y --no-install-recommends curl \
    && mkdir -p /usr/share/fonts/mplus \
    && curl -fsSL -o /usr/share/fonts/mplus/Mplus1-Regular.ttf https://github.com/coz-m/MPLUS_FONTS/raw/master/fonts/ttf/Mplus1-Regular.ttf \
    && curl -fsSL -o /usr/share/fonts/mplus/Mplus1-Bold.ttf https://github.com/coz-m/MPLUS_FONTS/raw/master/fonts/ttf/Mplus1-Bold.ttf \
    && curl -fsSL -o /usr/share/fonts/mplus/Mplus1Code-Regular.ttf https://github.com/coz-m/MPLUS_FONTS/raw/master/fonts/ttf/Mplus1Code-Regular.ttf \
    && fc-cache -f \
    && apt-get purge -y curl && apt-get autoremove -y \
    && rm -rf /var/lib/apt/lists/*

COPY local.conf /etc/fonts/local.conf
COPY --from=builder /app/chart_bot /usr/local/bin/chart_bot

ENV CHROME_PATH=/usr/bin/brave-browser-stable

CMD ["chart_bot"]
