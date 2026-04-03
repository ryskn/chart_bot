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
    && apt-get purge -y gnupg && apt-get autoremove -y \
    && rm -rf /var/lib/apt/lists/*

# M+ フォント（コミットSHA固定 + チェックサム検証）
ARG MPLUS_COMMIT=254bf30b2b631fb71153a22a822a7373c9256957
RUN mkdir -p /usr/share/fonts/mplus \
    && curl -fsSL -o /usr/share/fonts/mplus/Mplus1-Regular.ttf \
       https://raw.githubusercontent.com/coz-m/MPLUS_FONTS/${MPLUS_COMMIT}/fonts/ttf/Mplus1-Regular.ttf \
    && curl -fsSL -o /usr/share/fonts/mplus/Mplus1-Bold.ttf \
       https://raw.githubusercontent.com/coz-m/MPLUS_FONTS/${MPLUS_COMMIT}/fonts/ttf/Mplus1-Bold.ttf \
    && curl -fsSL -o /usr/share/fonts/mplus/Mplus1Code-Regular.ttf \
       https://raw.githubusercontent.com/coz-m/MPLUS_FONTS/${MPLUS_COMMIT}/fonts/ttf/Mplus1Code-Regular.ttf \
    && printf '%s  %s\n' \
       '81c723a512cc8497e1941312b8057f6a3419a434d13f3bde637f5e88a6843214' '/usr/share/fonts/mplus/Mplus1-Regular.ttf' \
       '6ea36d93d999d59c7d88c27bf58dd4b294793eab34d084b51968446d466f78c1' '/usr/share/fonts/mplus/Mplus1-Bold.ttf' \
       '4f9d2e95c909c2b348f204e5176e5205c68ef34584f214639d1e51f8a3402acf' '/usr/share/fonts/mplus/Mplus1Code-Regular.ttf' \
       | sha256sum -c - \
    && fc-cache -f \
    && apt-get purge -y curl && apt-get autoremove -y \
    && rm -rf /var/lib/apt/lists/*

COPY local.conf /etc/fonts/local.conf
COPY --from=builder /app/chart_bot /usr/local/bin/chart_bot

ENV CHROME_PATH=/usr/bin/brave-browser-stable

CMD ["chart_bot"]
