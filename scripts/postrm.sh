#!/bin/sh
# opencode2api postrm：purge（彻底卸载）时移除 service 文件与 manager.env；
# 保留 /var/lib/opencode2api 数据目录与 opencode2api 用户（误卸不可逆，数据不删）。

set -e

case "$1" in
  purge)
    rm -f /etc/systemd/system/opencode2api.service
    rm -f /etc/opencode2api/manager.env
    if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
      systemctl daemon-reload >/dev/null 2>&1 || true
    fi
    ;;
esac

exit 0
