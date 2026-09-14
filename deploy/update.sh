#!/bin/sh
# Update the installed binary; preserve service configuration and keep a backup.
set -eu
source_binary=${1:-"$(dirname "$0")/../bin/reviewd"}
target_binary=${2:-/opt/reviewd/bin/reviewd}
unit=${3:-reviewd.service}
if [ "$(id -u)" != 0 ]; then
    echo "Run with sudo: $0 [built-binary] [installed-binary] [unit]" >&2
    exit 1
fi
[ -x "$source_binary" ]
[ -f "$target_binary" ]
backup=$(mktemp "${target_binary}.backup.XXXXXXXX")
cp -p "$target_binary" "$backup"
install -m 0755 "$source_binary" "${target_binary}.next"
mv "${target_binary}.next" "$target_binary"
if systemctl restart "$unit" && systemctl is-active --quiet "$unit"; then
    echo "Updated $unit. Rollback binary: $backup"
else
    echo "Restart failed; restoring $backup" >&2
    cp -p "$backup" "${target_binary}.next"
    mv "${target_binary}.next" "$target_binary"
    systemctl restart "$unit"
    exit 1
fi
