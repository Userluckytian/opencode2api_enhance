pub mod commands;
pub mod config;
pub mod embed;
pub mod job;

use std::sync::Mutex;
use tauri::Manager;

/// 全局共享状态：桌面壳只保留「core 子进程 + 其 Job Object」两项生命周期资源
/// （v2.0.0 起其余壳内模块全部删除，业务职责在 Go core）。
pub struct AppState {
    /// Go core 管理器子进程（管理职责已移交 HTTP，壳负责拉起/随退出终止）
    pub core_child: Mutex<Option<std::process::Child>>,
    /// core 所在 Job Object：壳退出（含强杀）时自动终止 core 及其全部子进程，
    /// 杜绝孤儿进程 / 端口残留。Drop 时关闭句柄触发 KILL_ON_JOB_CLOSE。
    pub core_job: Mutex<Option<job::JobObject>>,
}

/// 桌面入口：释放内嵌二进制 → 拉起 core → 启动 Tauri（托盘常驻）
///
/// 端口规划：每个环境固定一段「槽位」，从 40000 向上、每槽 4100 宽。槽内布局：
///   base 管理器（Go core）、+80 免费层引擎、+180 提供方服务（provider）。
/// - 槽 0（40000）正式 release；槽 1（44100）dev（tauri dev）；槽 2（48200）便携测试包；
///   - 槽 3（52300）web-dev / headless（由 Go core 直跑，桌面壳不使用）
#[derive(Clone, Copy, PartialEq)]
enum EnvKind {
    Release,
    Dev,
    Portable,
    /// web-dev/headless 由 Go core 直跑，桌面壳不构造该变体（保留为槽位文档/未来使用）。
    #[allow(dead_code)]
    WebDev,
}

fn slot_base(kind: EnvKind) -> u16 {
    match kind {
        EnvKind::Release => 40000,
        EnvKind::Dev => 44100,
        EnvKind::Portable => 48200,
        EnvKind::WebDev => 52300,
    }
}

// 槽内偏移：+80 免费层引擎（env 名沿用 OPCODE2API_GATEWAY_PORT，与 Go core 契约一致）；
// +180 提供方服务（仅非 Release 环境注入，Release 用 Go 默认 50080）。
const OFF_GATEWAY: u16 = 80;
const OFF_PROVIDER: u16 = 180;

/// core 管理器实际端口：优先环境变量 OPCODE2API_MANAGER_PORT（手动覆盖），
/// 否则槽位基址 + 避让扫描（槽内前 32 个端口在控制区，通常立即可用）。
fn manager_port(kind: EnvKind) -> u16 {
    if let Ok(s) = std::env::var("OPCODE2API_MANAGER_PORT") {
        if let Ok(n) = s.trim().parse::<u16>() {
            return n;
        }
    }
    pick_free_port(slot_base(kind), 32)
}

/// 从 start 起扫描 budget 个端口，返回第一个当前空闲（无监听）的端口。
/// 用于避开同机其它环境已占用的管理器端口。
fn pick_free_port(start: u16, budget: u16) -> u16 {
    use std::time::Duration;
    for p in start..start.saturating_add(budget) {
        if let Ok(addr) = format!("127.0.0.1:{p}").parse() {
            if std::net::TcpStream::connect_timeout(&addr, Duration::from_millis(300)).is_err() {
                return p;
            }
        }
    }
    start
}

/// 清理 updater 在 %TEMP% 留下的旧安装包目录（仅匹配本应用前缀，且只删 1 小时前的）。
fn cleanup_stale_updater_dirs() {
    use std::time::{Duration, SystemTime};

    let Ok(entries) = std::fs::read_dir(std::env::temp_dir()) else {
        return;
    };
    let now = SystemTime::now();
    for entry in entries.flatten() {
        let name = entry.file_name();
        let name = name.to_string_lossy();
        if !name.starts_with("opencode2api-") || !name.contains("-updater-") {
            continue;
        }
        let Ok(meta) = entry.metadata() else { continue };
        if !meta.is_dir() {
            continue;
        }
        let Ok(modified) = meta.modified() else { continue };
        let Ok(age) = now.duration_since(modified) else {
            continue;
        };
        if age < Duration::from_secs(3600) {
            continue;
        }
        let _ = std::fs::remove_dir_all(entry.path());
    }
}

pub fn run() {
    // updater 会把安装包解压到 %TEMP%/opencode2api-<version>-updater-*（临时目录被刻意保留，
    // 供安装器在应用退出后继续运行）。启动时清理一小时前的残留，避免长期累积。
    cleanup_stale_updater_dirs();

    // --headless：无桌面（GTK/WinUI）模式，仅释放内嵌组件并拉起 core 管理器，
    // 供 SSH/服务器场景使用（管理界面走浏览器 http://127.0.0.1:<port>/）。
    // --headless 之后的参数原样透传给 core（如 -port/-listen，Go flag 后者覆盖前者）。
    let args: Vec<String> = std::env::args().skip(1).collect();
    let headless = args.iter().any(|a| a == "--headless");
    let core_extra: Vec<String> = args
        .iter()
        .filter(|a| a.as_str() != "--headless")
        .cloned()
        .collect();

    // 调试构建默认隔离数据目录：与正式版（%APPDATA%\opencode2api-manager）
    // 分开，避免配置/runtime 互相干扰。可用 OPCODE2API_DATA_DIR 显式覆盖。
    // 注意：环境变量存在但为空串时视为未设置（否则会静默回落共享生产目录）。
    if cfg!(debug_assertions) {
        let unset_or_empty = match std::env::var_os("OPCODE2API_DATA_DIR") {
            None => true,
            Some(v) => v.is_empty(),
        };
        if unset_or_empty {
            let base = dirs::config_dir().unwrap_or_else(|| std::path::PathBuf::from("."));
            // 单线程启动阶段设置环境变量，安全
            unsafe {
                std::env::set_var(
                    "OPCODE2API_DATA_DIR",
                    base.join("opencode2api-manager-dev"),
                );
            }
        }
    }
    // 调试构建默认开启 SSE 流信息输出（tauri dev 终端实时显示收发流，排查 IDE 解析问题）；
    // 正式版（release）不受影响。可用 OPCODE2API_SSE_DEBUG=0 关闭。
    if cfg!(debug_assertions) && std::env::var("OPCODE2API_SSE_DEBUG").is_err() {
        unsafe {
            std::env::set_var("OPCODE2API_SSE_DEBUG", "1");
        }
    }
    // 启动前释放内嵌子程序到 exe 旁 bin/ 目录
    let (_, binary_dir, _) = commands::manager_paths();
    match embed::ensure_binaries(&binary_dir) {
        Ok(wrote) => {
            if wrote {
                println!("已释放内置组件到 {}", binary_dir.display());
            }
        }
        Err(e) => eprintln!("警告: 释放内置组件失败: {}", e),
    }

    let (instances_path, binary_dir, _) = commands::manager_paths();
    let mut data_dir = instances_path
        .parent()
        .map(|p| p.to_path_buf())
        .unwrap_or_default();
    // 便携测试包隔离：exe 旁存在 portable.txt → 用独立数据目录，避免与正式版共用配置。
    let is_portable = binary_dir
        .parent()
        .map(|p| p.join("portable.txt").exists())
        .unwrap_or(false);
    let env_kind = if is_portable {
        EnvKind::Portable
    } else if cfg!(debug_assertions) {
        EnvKind::Dev
    } else {
        EnvKind::Release
    };
    if is_portable {
        let base = dirs::config_dir().unwrap_or_else(|| std::path::PathBuf::from("."));
        data_dir = base.join("opencode2api-manager-test");
        // 端口由槽位表（EnvKind::Portable → 48200 段）处理，见 spawn_core_manager。
    }

    // 壳只负责：释放内嵌二进制 → 以管理器方式拉起 core → 窗口承载 core 的 SPA。
    unsafe {
        std::env::set_var("OPCODE2API_DATA_DIR", &data_dir);
    }
    // 管理器端口：按环境槽位（40000+ 段），槽内避让扫描兜底。
    let mgr_port = manager_port(env_kind);
    // 透传参数里的 -port 会覆盖默认槽位端口（Go flag 后者覆盖前者），
    // core 实际监听值 = 覆盖值（若有）否则 mgr_port——健康检查与地址打印都须与之对齐。
    let effective_port = override_port(&core_extra).unwrap_or(mgr_port);
    let (core_child, core_job) =
        match spawn_core_manager(&data_dir, effective_port, env_kind, &core_extra) {
            Ok((child, job)) => (Some(child), job),
            Err(e) => {
                eprintln!("启动 core 管理器失败: {e}");
                (None, None)
            }
        };

    // headless：不构建 Tauri 窗口，直接阻塞等待 core
    if headless {
        run_headless(effective_port, core_child, core_job);
        return;
    }

    tauri::Builder::default()
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_updater::Builder::new().build())
        .plugin(tauri_plugin_process::init())
        .manage(AppState {
            core_child: Mutex::new(core_child),
            core_job: Mutex::new(core_job),
        })
        .invoke_handler(tauri::generate_handler![
            // 仅保留壳命令（二进制信息 / 窗口 / 托盘）；其余管理功能均由 Go core HTTP 承载
            commands::get_binaries_info,
            commands::hide_to_tray,
            commands::toggle_maximize,
            commands::quit_app
        ])
        .setup(move |app| {
            use tauri::Manager;

            // Linux/安装版：资源 dist 位于只读资源目录（deb: /usr/lib/opencode2api/bin/dist），
            // binary_dir 回退到配置目录时把 dist 复制过去，保证浏览器访问 core WebUI 可用。
            let (_, binary_dir, _) = commands::manager_paths();
            if let Ok(res) = app.path().resource_dir() {
                let src = res.join("bin").join("dist");
                let dst = binary_dir.join("dist");
                if src.join("index.html").exists() && !dst.join("index.html").exists() {
                    let _ = std::fs::create_dir_all(&dst);
                    let _ = copy_tree(&src, &dst);
                }
            }

            // 托盘菜单：右键显示「显示主窗口 / 退出」
            let show_i =
                tauri::menu::MenuItem::with_id(app, "show", "显示主窗口", true, None::<&str>)?;
            let quit_i =
                tauri::menu::MenuItem::with_id(app, "quit", "退出", true, None::<&str>)?;
            let menu = tauri::menu::Menu::with_items(app, &[&show_i, &quit_i])?;

            // 图标：取 built-in 窗口图标（由 tauri.conf bundle.icon 提供，打包后必有）
            let tray_icon = app.default_window_icon().cloned();

            let mut tray = tauri::tray::TrayIconBuilder::with_id("main-tray")
                .tooltip("opencode2api 管理器")
                .menu(&menu)
                .show_menu_on_left_click(true); // 左键单击也显示菜单（含退出），便于发现
            if let Some(icon) = tray_icon {
                tray = tray.icon(icon);
            }

            tray.on_menu_event(|app, event| match event.id().as_ref() {
                "show" => {
                    if let Some(w) = app.get_webview_window("main") {
                        let _ = w.show();
                        let _ = w.unminimize();
                        let _ = w.set_focus();
                    }
                }
                "quit" => {
                    // 只终止 core 子进程（Job Object 连带其全部子孙），再退出
                    if let Some(state) = app.try_state::<AppState>() {
                        if let Ok(mut ch) = state.core_child.lock() {
                            if let Some(mut c) = ch.take() {
                                let _ = c.kill();
                            }
                        }
                        // v2.0.0: 不再按旧 instances.json 的 PID 杀进程（PID 可能已被系统复用，误伤无关程序）。
                    }
                    app.exit(0);
                }
                _ => {}
            })
            .on_tray_icon_event(|tray, event| {
                // 左键单击显示窗口；右键由系统自动弹菜单（无需手动处理）
                if let tauri::tray::TrayIconEvent::Click {
                    button: tauri::tray::MouseButton::Left,
                    ..
                } = event
                {
                    let app = tray.app_handle();
                    if let Some(w) = app.get_webview_window("main") {
                        let _ = w.show();
                        let _ = w.unminimize();
                        let _ = w.set_focus();
                    }
                }
            })
            .build(app)?;

            // 窗口承载 core 管理器 SPA（core 已就绪；端口 = 启动时选定的 mgr_port）
            if let Some(w) = app.get_webview_window("main") {
                let url = format!("http://127.0.0.1:{effective_port}/");
                let _ = w.navigate(tauri::Url::parse(&url).expect("core manager url"));
            }
            Ok(())
        })
        .on_window_event(|window, event| {
            // 关闭窗口 = 最小化到托盘（core 继续运行）
            if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                let _ = window.hide();
            }
        })
        .build(tauri::generate_context!("tauri.conf.json"))
        .expect("桌面构建失败")
        .run(|app, event| {
            // 应用退出（托盘"退出"/quit_app）时终止 core 子进程，
            // 端口随之释放；Job Object 兜底清理其全部子孙进程。
            if let tauri::RunEvent::ExitRequested { .. } = event {
                if let Some(state) = app.try_state::<AppState>() {
                    if let Ok(mut ch) = state.core_child.lock() {
                        if let Some(mut c) = ch.take() {
                            let _ = c.kill();
                        }
                    }
                }
            }
        });
}

/// 从透传参数中解析用户显式指定的 -port（Go flag 后者覆盖前者，core 实际监听此值）。
/// 支持 `-port 28080` 与 `-port=28080`（及 --port 变体）；未指定/非法时返回 None（回落槽位端口）。
fn override_port(extra_args: &[String]) -> Option<u16> {
    let mut it = extra_args.iter().peekable();
    while let Some(a) = it.next() {
        if let Some(v) = a.strip_prefix("-port=").or_else(|| a.strip_prefix("--port=")) {
            if let Ok(p) = v.trim().parse::<u16>() {
                return Some(p);
            }
            continue;
        }
        if a == "-port" || a == "--port" {
            if let Some(v) = it.peek() {
                if let Ok(p) = v.trim().parse::<u16>() {
                    let _ = it.next();
                    return Some(p);
                }
            }
        }
    }
    None
}

/// 递归复制目录树（用于把只读资源目录的 dist 复制到可写 binary_dir）。
fn copy_tree(src: &std::path::Path, dst: &std::path::Path) -> std::io::Result<()> {
    for entry in std::fs::read_dir(src)? {
        let entry = entry?;
        let from = entry.path();
        let to = dst.join(entry.file_name());
        if from.is_dir() {
            std::fs::create_dir_all(&to)?;
            copy_tree(&from, &to)?;
        } else {
            std::fs::copy(&from, &to)?;
        }
    }
    Ok(())
}

/// headless 模式主循环：无 GUI（SSH/服务器场景），释放组件 → 拉起 core →
/// 复制前端资源（如需）→ 打印管理地址 → 阻塞等待 core 退出。
fn run_headless(
    mgr_port: u16,
    core_child: Option<std::process::Child>,
    core_job: Option<job::JobObject>,
) {
    // Linux/安装版：资源 dist 位于只读资源目录（deb: /usr/lib/opencode2api/bin/dist），
    // binary_dir 回退到配置目录时把 dist 复制过去，保证浏览器访问 core WebUI 可用。
    let (_, binary_dir, _) = commands::manager_paths();
    let dist_dst = binary_dir.join("dist");
    if !dist_dst.join("index.html").exists() {
        if let Ok(exe) = std::env::current_exe() {
            let exe_dir = exe.parent().unwrap_or(std::path::Path::new("/"));
            // 候选源：exe 旁 bin/dist（便携/开发）或 ../lib/opencode2api/bin/dist（deb）
            let candidates = [
                exe_dir.join("bin").join("dist"),
                exe_dir
                    .parent()
                    .unwrap_or(std::path::Path::new("/"))
                    .join("lib")
                    .join("opencode2api")
                    .join("bin")
                    .join("dist"),
            ];
            for src in candidates {
                if src.join("index.html").exists() {
                    let _ = std::fs::create_dir_all(&dist_dst);
                    if copy_tree(&src, &dist_dst).is_ok() {
                        println!("[headless] 已复制前端资源到 {}", dist_dst.display());
                    }
                    break;
                }
            }
        }
    }

    println!("[headless] 管理界面: http://127.0.0.1:{}/", mgr_port);
    println!("[headless] 按 Ctrl+C 退出（core 随本进程退出而终止）");

    let mut child = match core_child {
        Some(c) => c,
        None => {
            eprintln!("[headless] core 管理器未能启动，退出");
            return;
        }
    };
    // 阻塞等待 core 退出。Windows 上 JobObject 保证壳退出时 core 及其子进程被终止；
    // Linux 上壳退出后 core 继续运行（SSH 场景视为特性：服务不随终端断开而停止）。
    let _ = child.wait();
    drop(core_job);

    println!("[headless] core 已退出");
}

/// 拉起 Go core 管理器（bin/opencode2api -port <port> ...），等待 /health 就绪。
/// 数据目录经 OPCODE2API_DATA_DIR 注入（run() 已设置）。
/// 引擎/提供方端口按环境槽位注入 env（用户显式 OPCODE2API_*_PORT 优先）。
/// extra_args：追加到 core 命令行末尾（--headless 透传；Go flag 重复参数后者覆盖）。
fn spawn_core_manager(
    data_dir: &std::path::Path,
    port: u16,
    env_kind: EnvKind,
    extra_args: &[String],
) -> std::io::Result<(std::process::Child, Option<job::JobObject>)> {
    use std::io::{Read, Write};
    use std::process::Command;
    use std::time::Duration;

    let (_, binary_dir, _) = commands::manager_paths();
    let exe = commands::resolve_platform_bin(&binary_dir, "opencode2api");
    if !exe.exists() {
        return Err(std::io::Error::new(
            std::io::ErrorKind::NotFound,
            "bin/opencode2api 不存在（未释放内嵌组件）",
        ));
    }
    let cfg_path = data_dir.join("config.json");
    // 端口按环境槽位注入（槽位表：base+偏移）；用户显式环境变量优先。
    // 引擎端口额外支持 config.gateway_port：config 设置了有效端口时**不注入 env**，
    // 让 core 直接读 config（两者读同一 config.json）——若把 config 值固化进 env，
    // 用户之后在 WebUI 重置端口时 core 的 managerGatewayPort 会命中残留 env 旧值，
    // 恢复不到默认槽位（env > config > 默认）。仅 env 未设且 config 未设置时才注入槽位。
    let base = slot_base(env_kind);
    let slot = |off: u16| (base + off).to_string();
    if std::env::var_os("OPCODE2API_GATEWAY_PORT").is_none() {
        let from_config = crate::config::Config::load()
            .ok()
            .and_then(|c| c.gateway_port)
            .filter(|p| *p != 0);
        if from_config.is_none() {
            unsafe {
                std::env::set_var("OPCODE2API_GATEWAY_PORT", slot(OFF_GATEWAY));
            }
        }
    }
    // 提供方服务端口：仅非 Release 环境注入槽位值（Dev 44280 / Portable 48380），
    // Release 保持 Go 默认 50080。
    if env_kind != EnvKind::Release && std::env::var_os("OPCODE2API_PROVIDER_PORT").is_none() {
        unsafe {
            std::env::set_var("OPCODE2API_PROVIDER_PORT", slot(OFF_PROVIDER));
        }
    }
    let port_str = port.to_string();
    let mut cmd = Command::new(&exe);
    cmd.args(["-port", &port_str, "-password", "", "-config"])
        .arg(&cfg_path)
        .arg("-log-level")
        .arg("warn");
    if !extra_args.is_empty() {
        cmd.args(extra_args);
    }
    // 隐藏 core 子进程的控制台窗口
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        cmd.creation_flags(0x0800_0000); // CREATE_NO_WINDOW
    }
    let child = cmd.spawn()?;

    // 等待 /health 就绪（最多 ~15s）
    let addr = format!("127.0.0.1:{port}");
    let mut ready = false;
    for _ in 0..30 {
        std::thread::sleep(Duration::from_millis(500));
        if let Ok(mut stream) = std::net::TcpStream::connect(&addr) {
            let _ = stream.set_read_timeout(Some(Duration::from_secs(1)));
            let _ = stream.write_all(b"GET /health HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n");
            let mut buf = [0u8; 64];
            if stream.read(&mut buf).is_ok() && String::from_utf8_lossy(&buf).contains("200") {
                ready = true;
                break;
            }
        }
    }
    if !ready {
        eprintln!("警告: core 管理器 /health 未在预期时间内就绪（窗口可能先于服务加载）");
    }
    // 防孤儿进程：core 挂到 Job Object，壳退出（含强杀）时自动终止 core 及其子进程。
    let job = job::JobObject::new();
    if let Some(j) = &job {
        j.assign(&child);
    }
    Ok((child, job))
}
