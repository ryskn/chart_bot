ARG SYSBASE=quay.io/almalinuxorg/almalinux:10

FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o chart_bot .

FROM ${SYSBASE} AS system-build

RUN mkdir -p /mnt/sys-root; \
    dnf install --installroot /mnt/sys-root \
    coreutils-single glibc-minimal-langpack \
    --releasever 10 --setopt install_weak_deps=false --nodocs -y; \
    dnf --installroot /mnt/sys-root clean all

# EPEL + CRB有効化してchromium-headless + 日本語フォントをinstallrootに入れる
RUN dnf install -y epel-release && /usr/bin/crb enable; \
    dnf install --installroot /mnt/sys-root \
    chromium-headless \
    google-noto-sans-cjk-vf-fonts \
    nss \
    --releasever 10 --setopt install_weak_deps=false --nodocs -y; \
    dnf --installroot /mnt/sys-root clean all

# 不要ファイル削除
RUN rm -rf /mnt/sys-root/var/cache/dnf \
    /mnt/sys-root/var/log/dnf* \
    /mnt/sys-root/var/lib/dnf \
    /mnt/sys-root/var/log/yum.* \
    /mnt/sys-root/var/lib/rpm/* \
    /mnt/sys-root/usr/share/locale/en* \
    /mnt/sys-root/usr/share/doc \
    /mnt/sys-root/usr/share/man \
    /mnt/sys-root/boot; \
    touch /mnt/sys-root/etc/machine-id; \
    touch /mnt/sys-root/etc/resolv.conf

FROM scratch

COPY --from=system-build /mnt/sys-root/ /
COPY --from=builder /app/chart_bot /usr/local/bin/chart_bot

ENV CHROME_PATH=/usr/lib64/chromium-browser/headless_shell

CMD ["chart_bot"]
