#!/bin/sh
# opencode2api postinst：创建专用系统用户/数据目录，注册并启动 systemd 服务。

set -e

case "$1" in
  configure)
    # 1) 专用系统用户（不可登录；home 指向数据目录）
    if ! id -u opencode2api >/dev/null 2>&1; then
      useradd --system --home /var/lib/opencode2api --shell /usr/sbin/nologin \
        --comment "opencode2api manager service user" opencode2api
    fi

    # 2) 数据目录与配置目录（/etc/opencode2api 由 deb 的 files 装入 manager.env）
    mkdir -p /var/lib/opencode2api /etc/opencode2api
    chown -R opencode2api:opencode2api /var/lib/opencode2api
    # /etc/opencode2api/manager.env 由用户维护：opencode2api 只需读（不需要写权限）
    chown -R root:opencode2api /etc/opencode2api

    # 3) 注册并启动服务（start 容错：首次启动前用户未改端口也允许失败，
    #    不因端口冲突导致 dpkg 安装失败回滚）
    if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
      systemctl daemon-reload
      systemctl enable opencode2api >/dev/null 2>&1 || true
      systemctl start opencode2api >/dev/null 2>&1 || true
      echo ""
      echo "============================================================"
      echo "  opencode2api 安装完成，服务已注册并尝试启动！"
      echo ""
      echo "  1) 如需修改端口/监听地址/数据目录/网关密钥，请编辑："
      echo "       sudo nano /etc/opencode2api/manager.env"
      echo "  2) 修改 manager.env 后必须重启服务才生效："
      echo "       sudo systemctl daemon-reload && sudo systemctl restart opencode2api"
      echo "  3) 查看服务状态与实时日志："
      echo "       systemctl status opencode2api"
      echo "       journalctl -u opencode2api -f"
      echo ""
      echo "  管理 WebUI：http://127.0.0.1:${MANAGER_PORT:-60000}/"
      echo "============================================================"
    fi

    # 4) 首次安装提示（统一网关密钥默认 sk-unified-local；管理 WebUI 鉴权默认关闭
    #    （core 默认空密码），需要时在服务 ExecStart 加 -password）
    if [ -f /etc/opencode2api/manager.env ]; then
      if grep -q '^OPCODE2API_GATEWAY_KEY=' /etc/opencode2api/manager.env; then
        echo "opencode2api: 检测到 /etc/opencode2api/manager.env 显式设置了 OPCODE2API_GATEWAY_KEY"
        echo "opencode2api: 注意 env 优先级最高，会压过 WebUI「统一网关」卡片的修改；如非有意请注释该行（默认 sk-unified-local）"
      else
        echo "opencode2api: 统一网关密钥默认 sk-unified-local；如需自定义请在 WebUI「统一网关」卡片设置"
      fi
    fi

    # 5) 修改 manager.env 后生效提示：systemd EnvironmentFile 在服务启动时读取，
    #    改了端口/监听地址/网关密钥等必须 daemon-reload + restart 才生效。
    echo "opencode2api: 提示：修改 /etc/opencode2api/manager.env 后必须执行以下命令才生效："
    echo "opencode2api:   sudo systemctl daemon-reload && sudo systemctl restart opencode2api"
    echo "opencode2api: 当前服务状态：systemctl status opencode2api"
    ;;
esac

exit 0
