#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    let args: Vec<String> = std::env::args().collect();
    if args.len() > 1 && args[1] == "serve" {
        headless_main(&args);
    } else {
        opencode2api::run()
    }
}

/// headless 模式：仅启动 HTTP 服务（无窗口），默认 127.0.0.1:19090。
/// 支持 `serve --port <n>` 覆盖端口（亦可经 OPCODE2API_HTTP_PORT 环境变量）；
/// `serve --bind <addr>` 覆盖监听地址（默认仅回环；公网/局域网访问需显式
/// `--bind 0.0.0.0`，此时管理 API 无鉴权，请务必配合防火墙/反代限制来源）。
fn headless_main(args: &[String]) {
    use opencode2api::core::AppCore;
    use opencode2api::server;
    use std::sync::Arc;

    let port = args
        .iter()
        .position(|a| a == "--port")
        .and_then(|i| args.get(i + 1))
        .and_then(|p| p.parse::<u16>().ok())
        .or_else(|| {
            std::env::var("OPCODE2API_HTTP_PORT")
                .ok()
                .and_then(|p| p.parse().ok())
        })
        .unwrap_or(19090);
    let bind = args
        .iter()
        .position(|a| a == "--bind")
        .and_then(|i| args.get(i + 1))
        .cloned()
        .unwrap_or_else(|| "127.0.0.1".to_string());

    let core = Arc::new(AppCore::new());
    let rt = tokio::runtime::Runtime::new().expect("无法创建运行时");
    // 后台健康巡检（headless 模式同样生效；配置间隔为 0 时内部自动休眠）
    {
        let core_for_health = core.clone();
        rt.spawn(async move {
            opencode2api::health::health_loop(core_for_health).await;
        });
    }
    // 后台订阅自动拉取（headless 模式同样生效）
    {
        let core_for_sub = core.clone();
        rt.spawn(async move {
            opencode2api::subscribe::subscribe_loop(core_for_sub).await;
        });
    }
    // 同时等待：服务结束 或 收到 SIGTERM/SIGINT（Ctrl+C）。
    // 信号处理必须在退出前完成子进程清理，否则直接终止会残留
    // opencode2api/sing-box（桌面模式有 ExitRequested 兜底，headless 需显式处理）。
    let (core2, core3) = (core.clone(), core.clone());
    let serve_task = rt.spawn(async move {
        server::serve(&format!("{}:{}", bind, port), core2).await
    });
    rt.block_on(async {
        let signal = async {
            #[cfg(unix)]
            {
                use tokio::signal::unix::{signal, SignalKind};
                let mut term =
                    signal(SignalKind::terminate()).expect("注册 SIGTERM handler 失败");
                let mut int = signal(SignalKind::interrupt()).expect("注册 SIGINT handler 失败");
                tokio::select! {
                    _ = term.recv() => {}
                    _ = int.recv() => {}
                }
            }
            #[cfg(not(unix))]
            {
                let _ = tokio::signal::ctrl_c().await;
            }
        };
        tokio::select! {
            res = serve_task => {
                if let Err(e) = res {
                    eprintln!("Headless 服务异常: {}", e);
                }
            }
            _ = signal => {
                // 服务停止前先清理子进程
                stop_all_processes(&core3);
            }
        }
    });
    // 正常路径（serve 返回）也清理一次，覆盖 serve 因端口占用等立即返回的情况
    stop_all_processes(&core);
    rt.shutdown_timeout(std::time::Duration::from_secs(3));
}

/// 停止统一网关与全部实例进程（headless 退出清理，防止子进程残留占端口）。
fn stop_all_processes(core: &std::sync::Arc<opencode2api::core::AppCore>) {
    if let Ok(mut gateway) = core.gateway.lock() {
        gateway.stop();
    }
    if let Ok(mut mgr) = core.manager.lock() {
        let names: Vec<String> = mgr.list_instances().iter().map(|i| i.name.clone()).collect();
        for n in names {
            let _ = mgr.stop_instance(&n);
        }
    }
}
