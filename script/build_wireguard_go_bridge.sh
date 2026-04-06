#!/bin/sh

set -eu

# build_wireguard_go_bridge.sh - Builds WireGuardKitGo for Xcode.
#
# Xcode runs this via the PBXLegacyTarget "WireguardGoBridge". During archive
# the current working directory and BUILD_DIR layout are not guaranteed, so we
# resolve paths from PROJECT_DIR first and only then fall back to SourcePackages.

action="${1:-build}"
project_dir="${PROJECT_DIR:-$(pwd)}"

case "$action" in
    clean)
        make_target="clean"
        ;;
    build|install|installhdrs|archive)
        make_target="build"
        ;;
    *)
        make_target="build"
        ;;
esac

wireguard_go_dir=""

if [ -d "$project_dir/wireguard-apple/Sources/WireGuardKitGo" ]; then
    echo "Using local wireguard-apple checkout"
    wireguard_go_dir="$project_dir/wireguard-apple/Sources/WireGuardKitGo"
elif [ -n "${BUILD_DIR:-}" ]; then
    search_dir="$BUILD_DIR"
    while [ "$search_dir" != "/" ] && [ ! -d "$search_dir/SourcePackages/checkouts" ]; do
        search_dir=$(dirname "$search_dir")
    done

    if [ -d "$search_dir/SourcePackages/checkouts/wireguard-apple/Sources/WireGuardKitGo" ]; then
        echo "Using wireguard-apple from SourcePackages/checkouts/wireguard-apple"
        wireguard_go_dir="$search_dir/SourcePackages/checkouts/wireguard-apple/Sources/WireGuardKitGo"
    elif [ -d "$search_dir/SourcePackages/checkouts/Sources/WireGuardKitGo" ]; then
        echo "Using wireguard-apple from SourcePackages/checkouts"
        wireguard_go_dir="$search_dir/SourcePackages/checkouts/Sources/WireGuardKitGo"
    fi
fi

if [ -z "$wireguard_go_dir" ]; then
    echo "error: could not locate Sources/WireGuardKitGo" >&2
    echo "PROJECT_DIR=$project_dir" >&2
    echo "BUILD_DIR=${BUILD_DIR:-}" >&2
    exit 1
fi

export PATH="/opt/homebrew/bin:/usr/local/go/bin:/usr/local/bin:$PATH"

if ! command -v go >/dev/null 2>&1; then
    echo "error: Go toolchain not found in PATH=$PATH" >&2
    exit 1
fi

if ! command -v make >/dev/null 2>&1; then
    echo "error: make not found in PATH=$PATH" >&2
    exit 1
fi

echo "WireGuardKitGo dir: $wireguard_go_dir"
echo "Build action: $action -> make $make_target"

cd "$wireguard_go_dir"
exec make "$make_target"
