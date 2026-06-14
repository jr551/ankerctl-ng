#!/usr/bin/env bash
set -euo pipefail

APP_NAME="ankerctl-ng"
SERVICE_NAME="ankerctl-ng.service"
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUILD_OUT="${REPO_DIR}/dist/${APP_NAME}"
DEFAULT_CONFIG_DIR="${HOME}/.ankerctl-ng"

cmd="${1:-install}"

need_cmd() {
    command -v "$1" >/dev/null 2>&1
}

run_as_root() {
    if [ "$(id -u)" -eq 0 ]; then
        "$@"
    elif need_cmd sudo; then
        sudo "$@"
    else
        echo "Need root for: $*" >&2
        exit 1
    fi
}

detect_pkg_manager() {
    for tool in apt-get dnf yum pacman apk brew; do
        if need_cmd "$tool"; then
            echo "$tool"
            return 0
        fi
    done
    return 1
}

install_go_tools() {
    if need_cmd go && need_cmd git; then
        return 0
    fi
    pm="$(detect_pkg_manager || true)"
    if [ -z "${pm:-}" ]; then
        echo "Could not detect a supported package manager. Install Go and git manually." >&2
        exit 1
    fi
    echo "Installing Go build tools with ${pm}..."
    case "$pm" in
        apt-get)
            run_as_root apt-get update
            run_as_root apt-get install -y git curl ca-certificates golang-go build-essential
            ;;
        dnf)
            run_as_root dnf install -y git curl ca-certificates golang
            ;;
        yum)
            run_as_root yum install -y git curl ca-certificates golang
            ;;
        pacman)
            run_as_root pacman -Sy --noconfirm git curl ca-certificates go base-devel
            ;;
        apk)
            run_as_root apk add --no-cache git curl ca-certificates go build-base
            ;;
        brew)
            brew install go git
            ;;
    esac
}

build_binary() {
    mkdir -p "${REPO_DIR}/dist"
    (cd "${REPO_DIR}" && go mod download && go build -o "${BUILD_OUT}" ./cmd/ankerctl)
}

install_binary() {
    target="/usr/local/bin/${APP_NAME}"
    echo "Installing ${APP_NAME} to ${target}"
    run_as_root install -Dm755 "${BUILD_OUT}" "${target}"
}

write_service() {
    unit_path="/etc/systemd/system/${SERVICE_NAME}"
    user_name="${SUDO_USER:-$USER}"
    home_dir="$(eval echo "~${user_name}")"
    cat <<EOF | run_as_root tee "${unit_path}" >/dev/null
[Unit]
Description=ankerctl-ng experimental web UI
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${user_name}
WorkingDirectory=${REPO_DIR}
ExecStart=/usr/local/bin/${APP_NAME} --config ${DEFAULT_CONFIG_DIR} webserver --listen 0.0.0.0:4470
Restart=always
RestartSec=3
Environment=HOME=${home_dir}

[Install]
WantedBy=multi-user.target
EOF
    run_as_root systemctl daemon-reload
}

maybe_manage_service() {
    if ! need_cmd systemctl; then
        echo "systemd not available, skipping service setup."
        return 0
    fi
    if [ -t 0 ]; then
        read -r -p "Install or update ${SERVICE_NAME}? [y/N] " answer
        case "${answer}" in
            y|Y|yes|YES)
                write_service
                run_as_root systemctl enable --now "${SERVICE_NAME}"
                ;;
            *)
                ;;
        esac
    fi
}

restart_if_running() {
    if need_cmd systemctl && systemctl list-unit-files | grep -q "^${SERVICE_NAME}"; then
        echo "Restarting ${SERVICE_NAME}"
        run_as_root systemctl restart "${SERVICE_NAME}"
    fi
}

main() {
    case "${cmd}" in
        install)
            install_go_tools
            build_binary
            install_binary
            maybe_manage_service
            echo "Done. Run '${APP_NAME} webserver --listen 0.0.0.0:4470' to start manually."
            ;;
        update)
            install_go_tools
            build_binary
            install_binary
            write_service || true
            restart_if_running
            echo "Updated ${APP_NAME}."
            ;;
        *)
            echo "Usage: ./install.sh [install|update]" >&2
            exit 1
            ;;
    esac
}

main "$@"
