#!/bin/sh
# opencode2api preinst：安装/升级前停掉残留旧服务，避免端口冲突。
# 仅在已启用旧服务时操作，静默失败无害（set -e 下 systemctl 查询不命中会返回非 0，
# 故用 `|| true` 包裹；本脚本以幂等为原则）。

set -e

case "$1" in
  install|upgrade)
    if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
      systemctl stop opencode2api >/dev/null 2>&1 || true
      systemctl disable opencode2api >/dev/null 2>&1 || true
    fi
    ;;
esac

exit 0
