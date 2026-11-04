//! Tauri command 层（v2.0.0 最小集）：仅保留壳自身能力——
//! 内嵌二进制信息 + 窗口/托盘控制。业务管理全部走 Go core HTTP（前端 invoke 已移除）。

use crate::config::Config;
use serde::Serialize;
use std::path::{Path, PathBuf};

// ======================== 路径 ========================

/// 返回（数据目录下的 instances.json 路径、二进制目录、runtime 目录）。
/// instances.json 在 v2.0.0 已不再读写，这里仅作为「数据目录」的定位锚点。
pub fn manager_paths() -> (PathBuf, PathBuf, PathBuf) {
    let config_dir = Config::config_dir();
    let instances_path = config_dir.join("instances.json");
    let exe_bin = std::env::current_exe()
        .ok()
        .and_then(|p| p.parent().map(|d| d.to_path_buf()))
        .unwrap_or_else(|| PathBuf::from("."))
        .join("bin");
    // 安装版（deb /usr/bin、NSIS perMachine、macOS dmg）exe 所在目录只读：
    // exe 旁 bin/ 不可写时回退到配置目录下的 bin/，保证内嵌组件（core / 引擎）
    // 能正常释放（否则普通用户下释放失败，管理器不可用）。
    let binary_dir = if writable_dir(&exe_bin) {
        exe_bin
    } else {
        config_dir.join("bin")
    };
    let runtime_dir = config_dir.join("runtime");
    (instances_path, binary_dir, runtime_dir)
}

/// 目录可写性探测：能创建目录、且能写入并删除探针文件视为可写。
fn writable_dir(dir: &Path) -> bool {
    if std::fs::create_dir_all(dir).is_err() {
        return false;
    }
    let probe = dir.join(".write_probe");
    if std::fs::write(&probe, b"").is_err() {
        return false;
    }
    let _ = std::fs::remove_file(&probe);
    true
}

/// 解析子程序可执行文件路径：Windows 优先 `<name>.exe`，其他平台优先无扩展名
/// `<name>`（与 embed.rs 的 platform_name 释放逻辑对应，避免 Linux 误选残留的
/// Windows PE .exe 而无法执行）。
pub fn resolve_platform_bin(bin_dir: &Path, name: &str) -> PathBuf {
    let (preferred, fallback) = if cfg!(windows) {
        (bin_dir.join(format!("{}.exe", name)), bin_dir.join(name))
    } else {
        (bin_dir.join(name), bin_dir.join(format!("{}.exe", name)))
    };
    if preferred.exists() {
        preferred
    } else {
        fallback
    }
}

// ======================== 二进制信息 ========================

#[derive(Debug, Serialize)]
pub struct BinariesInfo {
    pub bin_dir: String,
    /// Go core 管理器（40000）
    pub oc_exists: bool,
    /// 免费层引擎（40080，opencode2api-win）
    pub engine_exists: bool,
}

/// 前端 Settings 页展示用：内嵌组件释放情况。
#[tauri::command]
pub fn get_binaries_info() -> BinariesInfo {
    let (_, binary_dir, _) = manager_paths();
    BinariesInfo {
        bin_dir: binary_dir.to_string_lossy().to_string(),
        oc_exists: resolve_platform_bin(&binary_dir, "opencode2api").exists(),
        engine_exists: resolve_platform_bin(&binary_dir, "opencode2api-win").exists(),
    }
}

// ======================== 窗口控制（托盘） ========================

/// 收起到托盘（前端关闭按钮调用）
#[tauri::command]
pub fn hide_to_tray(app: tauri::AppHandle) {
    use tauri::Manager;
    if let Some(w) = app.get_webview_window("main") {
        let _ = w.hide();
    }
}

/// 退出进程（前端红点确认后调用）
#[tauri::command]
pub fn quit_app(app: tauri::AppHandle) {
    app.exit(0);
}

/// 最大化/还原（前端交通灯按钮调用）
#[tauri::command]
pub fn toggle_maximize(app: tauri::AppHandle) {
    use tauri::Manager;
    if let Some(w) = app.get_webview_window("main") {
        if let Ok(max) = w.is_maximized() {
            if max {
                let _ = w.unmaximize();
            } else {
                let _ = w.maximize();
            }
        }
    }
}
