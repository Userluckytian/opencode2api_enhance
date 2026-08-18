#!/bin/sh
# opencode2api prerm：卸载/升级前停止并禁用服务，释放端口。

set -e

case "$1" in
  remove|upgrade|deconfigure)
    if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
      systemctl stop opencode2api >/dev/null 2>&1 || true
      systemctl disable opencode2api >/dev/null 2>&1 || true
    fi
    ;;
esac

exit 0
