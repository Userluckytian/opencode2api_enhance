//! Tauri command 层：替代原 enhance 的 axum Web API。
//! 所有前端交互经由 #[tauri::command] invoke 进入本模块。

use crate::AppState;
use crate::clash_yaml;
use crate::config::Config;
use crate::core::AppCore;
#[cfg(windows)]
use crate::instance::no_window;
use crate::instance::{Instance, InstanceManager};
use crate::probe::{DEFAULT_PROBE_API_PORT, DEFAULT_PROBE_SOCKS_PORT};
use serde::{Deserialize, Serialize};
use serde_json::json;
use std::collections::{HashSet, VecDeque};
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::thread;

// ======================== 路径与共享状态 ========================

pub fn manager_paths() -> (PathBuf, PathBuf, PathBuf) {
    let config_dir = Config::config_dir();
    let instances_path = config_dir.join("instances.json");
    let binary_dir = std::env::current_exe()
        .ok()
        .and_then(|p| p.parent().map(|d| d.to_path_buf()))
        .unwrap_or_else(|| PathBuf::from("."))
        .join("bin");
    let runtime_dir = config_dir.join("runtime");
    (instances_path, binary_dir, runtime_dir)
}

pub fn create_manager() -> InstanceManager {
    let (instances_path, binary_dir, runtime_dir) = manager_paths();
    let mut manager = InstanceManager::new(instances_path, binary_dir, runtime_dir);
    let _ = manager.load();
    // 启动即校正：清理"进程已死但状态残留 Running"的僵尸状态
    let _ = manager.reconcile_states();
    manager
}

fn default_password() -> String {
    Config::effective_default_password()
}

// ======================== 统一网关（F1） ========================

/// 同步统一网关：根据「运行中且 join_gateway=true」的实例集合更新网关池。
pub fn sync_gateway_core(core: &AppCore) {
    let instances = core
        .manager
        .lock()
        .map(|mut mgr| {
            let _ = mgr.reconcile_states();
            mgr.list_instances().to_vec()
        })
        .unwrap_or_default();
    if let Ok(mut gateway) = core.gateway.lock() {
        if let Err(e) = gateway.sync(&instances) {
            eprintln!("统一网关同步失败: {}", e);
        }
    }
}

pub fn sync_gateway(state: &tauri::State<'_, AppState>) {
    sync_gateway_core(&state.core);
}

pub fn gateway_status_core(core: &AppCore) -> Result<crate::gateway::GatewayStatus, String> {
    let total_instances = core
        .manager
        .lock()
        .map_err(|_| "状态锁失败".to_string())?
        .list_instances()
        .iter()
        .filter(|i| i.join_gateway)
        .count();
    let mut gateway = core.gateway.lock().map_err(|_| "网关锁失败".to_string())?;
    Ok(gateway.status(total_instances))
}

#[tauri::command]
pub fn gateway_status(
    state: tauri::State<'_, AppState>,
) -> Result<crate::gateway::GatewayStatus, String> {
    gateway_status_core(&state.core)
}

/// 切换网关路由模式（smart / failover / round_robin）：写入网关配置并重启网关进程。
/// smart（默认）= failover 游标 + 健康计数/坏池/超时切换完整容错。
pub fn gateway_set_route_mode_core(core: &AppCore, mode: &str) -> Result<(), String> {
    if mode != "smart" && mode != "failover" && mode != "round_robin" {
        return Err("路由模式仅支持 smart / failover / round_robin".to_string());
    }
    let instances = core
        .manager
        .lock()
        .map_err(|_| "状态锁失败".to_string())?
        .list_instances()
        .to_vec();
    let mut gateway = core.gateway.lock().map_err(|_| "网关锁失败".to_string())?;
    gateway.set_route_mode(mode);
    gateway.stop();
    gateway
        .sync(&instances)
        .map_err(|e| format!("切换路由模式失败: {}", e))
}

#[tauri::command]
pub fn gateway_set_route_mode(
    state: tauri::State<'_, AppState>,
    mode: String,
) -> Result<(), String> {
    gateway_set_route_mode_core(&state.core, &mode)
}

/// 关闭统一网关：停止网关进程、清空池（实例的 join_gateway 标记保留，重启后可恢复）。
pub fn gateway_stop_core(core: &AppCore) -> Result<(), String> {
    let mut gateway = core.gateway.lock().map_err(|_| "网关锁失败".to_string())?;
    gateway.stop();
    Ok(())
}

#[tauri::command]
pub fn gateway_stop(state: tauri::State<'_, AppState>) -> Result<(), String> {
    gateway_stop_core(&state.core)
}

/// 切换实例是否加入统一网关池（join_gateway），并同步网关。
pub fn set_join_gateway_core(core: &AppCore, name: &str, join: bool) -> Result<(), String> {
    let mut mgr = core.manager.lock().map_err(|_| "状态锁失败".to_string())?;
    let _ = mgr.load();
    mgr.set_join_gateway(name, join)
        .map_err(|e| e.to_string())?;
    mgr.save_state().map_err(|e| e.to_string())?;
    let instances = mgr.list_instances().to_vec();
    drop(mgr);
    if let Ok(mut gateway) = core.gateway.lock() {
        gateway
            .sync(&instances)
            .map_err(|e| format!("同步网关失败: {}", e))?;
    }
    Ok(())
}

#[tauri::command]
pub fn set_join_gateway(
    state: tauri::State<'_, AppState>,
    name: String,
    join: bool,
) -> Result<(), String> {
    set_join_gateway_core(&state.core, &name, join)
}

// ======================== 响应结构 ========================

#[derive(Debug, Serialize)]
pub struct NodeView {
    pub name: String,
    pub node_type: String,
    pub server: String,
    pub port: u16,
    pub has_cred: bool,
    pub group: String,
}

#[derive(Debug, Deserialize)]
pub struct BatchAddItem {
    pub node: String,
    pub name: Option<String>,
    pub port: Option<u16>,
}

#[derive(Debug, Serialize)]
pub struct BatchAddResult {
    pub added: Vec<serde_json::Value>,
    pub errors: Vec<serde_json::Value>,
    pub added_count: usize,
    pub error_count: usize,
}

#[derive(Debug, Serialize)]
pub struct BatchOpResult {
    pub success: Vec<String>,
    pub errors: serde_json::Map<String, serde_json::Value>,
    pub success_count: usize,
    pub error_count: usize,
}

/// 一键重启结果：停止/启动实例数 + 强制释放的端口列表。
#[derive(Debug, Serialize)]
pub struct RestartPoolResult {
    pub stopped: usize,
    pub started: usize,
    pub freed_ports: Vec<u16>,
    pub gateway_running: bool,
    pub error: Option<String>,
}

#[derive(Debug, Serialize)]
pub struct PortCheckResult {
    pub available: bool,
    pub reason: String,
}

#[derive(Debug, Serialize)]
pub struct ConfigView {
    pub base_url: String,
    pub default_password: String,
    pub has_password: bool,
    pub clash_external_url: String,
    pub has_clash_token: bool,
    pub timeout_ttft_min_ms: i64,
    pub timeout_ttft_max_ms: i64,
    pub timeout_silence_min_ms: i64,
    pub timeout_silence_max_ms: i64,
    pub failover_probe_min: i64,
    pub failover_probe_max: i64,
    pub call_log_max: i64,
    pub show_node_prefix: bool,
    pub gateway_port: u16,
    pub gateway_key: String,
    pub has_gateway_key: bool,
    pub http_port: u16,
    pub subscribe_url: String,
    pub subscribe_interval_min: u32,
    pub health_check_interval_sec: u32,
    pub health_restart_threshold: u32,
    pub log_filter_keywords: String,
}

#[derive(Debug, Serialize)]
pub struct BinariesInfo {
    pub bin_dir: String,
    pub oc_exists: bool,
    pub sb_exists: bool,
    /// 当前运行平台（"windows"/"linux"/"macos"），前端据此展示正确的
    /// 子程序文件名与开机自启说明（Linux 下无 .exe 后缀）。
    pub platform: String,
}

// ======================== 节点 ========================

/// 列出全部节点（外部控制 API 优先 + 本地 Clash Verge profiles 补充）
pub fn list_nodes_core() -> Result<Vec<NodeView>, String> {
    match clash_yaml::list_nodes_with_group() {
        Ok(nodes) => Ok(nodes
            .into_iter()
            .map(|n| NodeView {
                has_cred: n.password.is_some() || n.uuid.is_some(),
                name: n.name,
                node_type: n.node_type,
                server: n.server,
                port: n.port,
                group: n.group,
            })
            .collect()),
        Err(e) => Err(e.to_string()),
    }
}

#[tauri::command]
pub fn list_nodes() -> Result<Vec<NodeView>, String> {
    list_nodes_core()
}

/// 从订阅缓存中删除节点（按名称），返回实际删除数量。
pub fn delete_node_core(name: &str) -> Result<usize, String> {
    crate::subscribe::remove_subscription_node(name)
}
pub fn delete_nodes_core(names: Vec<String>) -> Result<usize, String> {
    crate::subscribe::remove_subscription_nodes(&names)
}

#[tauri::command]
pub fn delete_node(name: String) -> Result<usize, String> {
    delete_node_core(&name)
}

#[tauri::command]
pub fn delete_nodes(names: Vec<String>) -> Result<usize, String> {
    delete_nodes_core(names)
}

/// 查询节点在 Clash 配置中的地址（server:port），供实例 IP 列展示
fn node_ip(node_name: &str) -> String {
    clash_yaml::list_nodes_with_group()
        .ok()
        .and_then(|ns| {
            ns.iter()
                .find(|n| n.name == node_name)
                .map(|n| format!("{}:{}", n.server, n.port))
        })
        .unwrap_or_default()
}

/// 随机生成 sk- 开头的实例密钥（无外部随机库，用时间种子+LCG）
pub(crate) fn gen_sk_key() -> String {
    let seed = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos() as u128)
        .unwrap_or(0)
        ^ ((std::process::id() as u128) << 64);
    let mut s = seed;
    let mut out = String::from("sk-");
    const CHARS: &[u8] = b"abcdefghijklmnopqrstuvwxyz0123456789";
    for _ in 0..16 {
        s = s
            .wrapping_mul(6364136223846793005)
            .wrapping_add(1442695040888963407);
        out.push(CHARS[((s >> 40) % 36) as usize] as char);
    }
    out
}

/// 停止全部实例（应用退出时调用）：
/// 遍历所有实例，只要有 pid（opencode2api / sing-box）就尽力杀掉，
/// 不依赖状态记录——即使状态因异常退出显示为 Stopped，残留进程也会被清理，
/// 确保实例占用的端口（API 端口 / sing-box 端口）在软件退出后全部释放。
pub fn stop_all_instances(state: &tauri::State<'_, AppState>) {
    stop_all_instances_core(&state.core);
}

pub fn stop_all_instances_core(core: &AppCore) {
    let Ok(mut mgr) = core.manager.lock() else {
        return;
    };
    let _ = mgr.load();
    let names: Vec<String> = mgr
        .list_instances()
        .iter()
        .filter(|i| {
            i.pid.is_some()
                || i.singbox_pid.is_some()
                || i.status == crate::instance::InstanceStatus::Running
                || i.status == crate::instance::InstanceStatus::Starting
        })
        .map(|i| i.name.clone())
        .collect();
    for n in names {
        let _ = mgr.stop_instance(&n);
    }
}

// ======================== 清除数据（缓存/实例/配置） ========================

/// 清除本地数据。level 语义：
/// 1 = 仅运行数据（runtime 目录：日志/stats/临时生成的配置），保留配置与实例记录
/// 2 = 运行数据 + 实例记录（instances.json 清空，回到空实例池）
/// 3 = 全部重置（运行数据 + 实例 + config.json，回到出厂默认）
/// 清理前先停掉所有运行中实例与统一网关，避免残留进程占用端口。
pub fn data_clean_core(core: &AppCore, level: u8) -> Result<(), String> {
    if level != 1 && level != 2 && level != 3 {
        return Err(format!("无效的清理级别: {}", level));
    }

    // 先停后清：关闭统一网关 + 所有实例进程（含状态异常但残留的 pid）
    if let Ok(mut gateway) = core.gateway.lock() {
        gateway.stop();
    }
    stop_all_instances_core(core);
    // 稍等进程退出，释放端口
    std::thread::sleep(std::time::Duration::from_millis(300));

    let config_dir = Config::config_dir();
    clean_data_at(&config_dir, level)?;

    // 清空管理器内存里的实例状态，保证前端刷新即见空
    if let Ok(mut mgr) = core.manager.lock() {
        mgr.instances.clear();
        let _ = mgr.load(); // 重新读取 instances.json（level>=2 时为 []，level=1 时仍为原列表）
    }

    Ok(())
}

#[tauri::command]
pub fn data_clean(state: tauri::State<'_, AppState>, level: u8) -> Result<(), String> {
    data_clean_core(&state.core, level)
}

/// 在指定数据目录执行清理（纯 fs 逻辑，便于单元测试）。
fn clean_data_at(config_dir: &std::path::Path, level: u8) -> Result<(), String> {
    if level != 1 && level != 2 && level != 3 {
        return Err(format!("无效的清理级别: {}", level));
    }

    let runtime_dir = config_dir.join("runtime");

    // 1) 删除 runtime 目录（运行数据）
    if runtime_dir.exists() {
        std::fs::remove_dir_all(&runtime_dir).map_err(|e| format!("删除运行数据失败: {}", e))?;
    }

    let instances_path = config_dir.join("instances.json");

    // 2) 清空实例记录（回到空实例池）
    if level >= 2 && instances_path.exists() {
        std::fs::write(&instances_path, "[]").map_err(|e| format!("清空实例记录失败: {}", e))?;
    }

    // 3) 删除配置（回到出厂默认），并备份一份便于误操作恢复
    if level == 3 {
        let config_path = config_dir.join("config.json");
        if config_path.exists() {
            let backup = config_dir.join("config.json.bak");
            let _ = std::fs::copy(&config_path, &backup);
            std::fs::remove_file(&config_path).map_err(|e| format!("删除配置失败: {}", e))?;
        }
    }

    Ok(())
}

#[cfg(test)]
mod clean_tests {
    use super::*;
    use std::fs;

    fn temp_dir() -> std::path::PathBuf {
        static N: std::sync::atomic::AtomicU32 = std::sync::atomic::AtomicU32::new(0);
        let n = N.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
        let dir =
            std::env::temp_dir().join(format!("oc2api-clean-test-{}-{}", std::process::id(), n));
        fs::create_dir_all(&dir).ok();
        dir
    }

    #[test]
    fn test_clean_level1_keeps_instances_and_config() {
        let dir = temp_dir();
        fs::create_dir_all(dir.join("runtime")).ok();
        fs::write(dir.join("instances.json"), r#"[{"name":"a"}]"#).ok();
        fs::write(dir.join("config.json"), r#"{"base_url":"x"}"#).ok();

        clean_data_at(&dir, 1).unwrap();

        assert!(!dir.join("runtime").exists(), "runtime 应被删除");
        assert!(dir.join("instances.json").exists(), "实例记录应保留");
        assert!(dir.join("config.json").exists(), "配置应保留");
        fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn test_clean_level2_clears_instances_keeps_config() {
        let dir = temp_dir();
        fs::create_dir_all(dir.join("runtime")).ok();
        fs::write(dir.join("instances.json"), r#"[{"name":"a"}]"#).ok();
        fs::write(dir.join("config.json"), r#"{"base_url":"x"}"#).ok();

        clean_data_at(&dir, 2).unwrap();

        assert!(!dir.join("runtime").exists());
        let instances = fs::read_to_string(dir.join("instances.json")).unwrap();
        assert_eq!(instances.trim(), "[]", "实例记录应清空");
        assert!(dir.join("config.json").exists(), "配置应保留");
        fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn test_clean_level3_resets_everything_with_backup() {
        let dir = temp_dir();
        fs::create_dir_all(dir.join("runtime")).ok();
        fs::write(dir.join("instances.json"), r#"[{"name":"a"}]"#).ok();
        fs::write(dir.join("config.json"), r#"{"base_url":"x"}"#).ok();

        clean_data_at(&dir, 3).unwrap();

        assert!(!dir.join("runtime").exists());
        assert!(!dir.join("config.json").exists(), "配置应删除");
        assert!(dir.join("config.json.bak").exists(), "应有备份");
        let instances = fs::read_to_string(dir.join("instances.json")).unwrap();
        assert_eq!(instances.trim(), "[]");
        fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn test_clean_invalid_level_rejected() {
        let dir = temp_dir();
        assert!(clean_data_at(&dir, 0).is_err());
        assert!(clean_data_at(&dir, 4).is_err());
        fs::remove_dir_all(&dir).ok();
    }
}

// ======================== 实例 CRUD ========================

pub fn list_instances_core(core: &AppCore) -> Result<Vec<Instance>, String> {
    let mut mgr = core.manager.lock().map_err(|_| "状态锁失败".to_string())?;
    let _ = mgr.load();
    // 首次加载时校正状态，保证前端显示与真实进程一致
    let _ = mgr.reconcile_states();
    Ok(mgr.list_instances().to_vec())
}

#[tauri::command]
pub fn list_instances(state: tauri::State<'_, AppState>) -> Result<Vec<Instance>, String> {
    list_instances_core(&state.core)
}

/// 手动刷新：只校正指定名称的实例状态，返回这些实例的最新状态。
/// 前端分批（每批少量并发）调用，按返回数量累计显示进度。
pub fn refresh_states_core(core: &AppCore, names: Vec<String>) -> Result<Vec<Instance>, String> {
    let mut mgr = core.manager.lock().map_err(|_| "状态锁失败".to_string())?;
    let _ = mgr.load();
    mgr.reconcile_batch(&names).map_err(|e| e.to_string())
}

#[tauri::command]
pub fn refresh_states(
    state: tauri::State<'_, AppState>,
    names: Vec<String>,
) -> Result<Vec<Instance>, String> {
    refresh_states_core(&state.core, names)
}

/// 生成不重复的实例名：实例1、实例2…
fn next_auto_name(mgr: &InstanceManager) -> String {
    let used: std::collections::HashSet<String> = mgr
        .list_instances()
        .iter()
        .map(|i| i.name.clone())
        .collect();
    let mut n = 1u32;
    loop {
        let candidate = format!("实例{}", n);
        if !used.contains(&candidate) {
            return candidate;
        }
        n += 1;
    }
}

pub fn add_instance_core(
    core: &AppCore,
    name: String,
    port: u16,
    node: String,
    password: String,
) -> Result<Instance, String> {
    if node.trim().is_empty() {
        return Err("节点不能为空".to_string());
    }
    if port < 1024 {
        return Err("端口需 >= 1024".to_string());
    }
    let ip = node_ip(&node.trim());
    let mut mgr = core.manager.lock().map_err(|_| "状态锁失败".to_string())?;
    let _ = mgr.load();
    let final_name = if name.trim().is_empty() {
        next_auto_name(&mgr)
    } else {
        // 实例名直接用于 runtime_dir.join(name) 创建目录：必须经 sanitize
        // 去除路径分隔符/控制字符，防止 ../ 等目录穿越。
        sanitize_instance_name(&name)
    };
    let sk = if password.trim().is_empty() {
        gen_sk_key()
    } else {
        password.trim().to_string()
    };
    mgr.add_instance(final_name.clone(), port, node.trim().to_string(), sk, ip)
        .map_err(|e| e.to_string())?;
    Ok(mgr
        .list_instances()
        .iter()
        .find(|i| i.name == final_name)
        .cloned()
        .ok_or_else(|| "实例创建后未找到".to_string())?)
}

#[tauri::command]
pub fn add_instance(
    state: tauri::State<'_, AppState>,
    name: String,
    port: u16,
    node: String,
    password: String,
) -> Result<Instance, String> {
    add_instance_core(&state.core, name, port, node, password)
}

pub fn remove_instance_core(core: &AppCore, name: &str) -> Result<(), String> {
    let mut mgr = core.manager.lock().map_err(|_| "状态锁失败".to_string())?;
    let _ = mgr.load();
    mgr.remove_instance(&name).map_err(|e| e.to_string())?;
    mgr.save_state().map_err(|e| e.to_string())?;
    let instances = mgr.list_instances().to_vec();
    drop(mgr);
    // 同步网关：移除的实例若在池中，需从代理池剔除
    if let Ok(mut gateway) = core.gateway.lock() {
        gateway
            .sync(&instances)
            .map_err(|e| format!("同步网关失败: {}", e))?;
    }
    Ok(())
}

#[tauri::command]
pub fn remove_instance(state: tauri::State<'_, AppState>, name: String) -> Result<(), String> {
    remove_instance_core(&state.core, &name)
}

// ======================== 实例启停（阻塞，走 spawn_blocking） ========================

pub fn start_instance_core(core: &AppCore, name: &str) -> Result<(), String> {
    let manager = Arc::clone(&core.manager);
    let gateway = Arc::clone(&core.gateway);
    (|| {
        // 短锁：标记 Starting 并取出实例快照
        let (instance, binary_dir, runtime_dir) = {
            let mut mgr = manager.lock().map_err(|_| "状态锁失败".to_string())?;
            let _ = mgr.load();
            let instance = mgr
                .mark_starting(&name)
                .map_err(|error| error.to_string())?;
            (instance, mgr.binary_dir.clone(), mgr.runtime_dir.clone())
        };
        // 放锁：执行实际启动（临时 manager，不持共享锁）
        let outcome = crate::instance::start_instance_process(instance, &binary_dir, &runtime_dir);
        // 短锁：回写结果
        let mut mgr = manager.lock().map_err(|_| "状态锁失败".to_string())?;
        let apply_result = mgr.apply_start_result(&name, outcome);
        let save_result = mgr.save_state().map_err(|error| error.to_string());
        let result = match apply_result {
            Ok(()) => save_result,
            Err(error) => {
                let _ = save_result;
                Err(error.to_string())
            }
        };
        // 同步网关（池成员可能变化）
        let _ = mgr.reconcile_states();
        if let Ok(mut g) = gateway.lock() {
            let _ = g.sync(mgr.list_instances());
        }
        result
    })()
}

#[tauri::command]
pub async fn start_instance(state: tauri::State<'_, AppState>, name: String) -> Result<(), String> {
    let core = state.core.clone();
    tokio::task::spawn_blocking(move || start_instance_core(&core, &name))
        .await
        .map_err(|e| format!("启动实例任务失败: {}", e))?
}

pub fn stop_instance_core(core: &AppCore, name: &str) -> Result<(), String> {
    let manager = Arc::clone(&core.manager);
    let gateway = Arc::clone(&core.gateway);
    (|| {
        // 短锁：标记 Stopping 并取出 PID
        let (pid, singbox_pid) = {
            let mut mgr = manager.lock().map_err(|_| "状态锁失败".to_string())?;
            let _ = mgr.load();
            let pids = mgr.prepare_stop(&name).map_err(|error| error.to_string())?;
            mgr.save_state().map_err(|error| error.to_string())?;
            pids
        };
        // 放锁：杀进程
        if let Some(pid) = pid {
            let _ = crate::instance::kill_process(pid);
        }
        if let Some(singbox_pid) = singbox_pid {
            let _ = crate::instance::kill_process(singbox_pid);
        }
        // 短锁：回写停止状态
        let mut mgr = manager.lock().map_err(|_| "状态锁失败".to_string())?;
        let result = mgr.finish_stop(&name).map_err(|error| error.to_string());
        mgr.save_state().map_err(|error| error.to_string())?;
        // 同步网关（池成员可能变化）
        let _ = mgr.reconcile_states();
        if let Ok(mut g) = gateway.lock() {
            let _ = g.sync(mgr.list_instances());
        }
        result
    })()
}

#[tauri::command]
pub async fn stop_instance(state: tauri::State<'_, AppState>, name: String) -> Result<(), String> {
    let core = state.core.clone();
    tokio::task::spawn_blocking(move || stop_instance_core(&core, &name))
        .await
        .map_err(|e| format!("停止实例任务失败: {}", e))?
}

pub fn test_instance_core(
    core: &AppCore,
    name: &str,
) -> Result<crate::instance::TestResult, String> {
    let manager = Arc::clone(&core.manager);
    (|| {
        let mut mgr = manager.lock().map_err(|_| "状态锁失败".to_string())?;
        let _ = mgr.load();
        let name_owned = name.to_string();
        let port = mgr.prepare_test(&name_owned).map_err(|e| e.to_string())?;
        // 启用 401 门禁后，自检需带实例密钥；实例未设密码时回退全局默认密码
        let auth = mgr.find_instance(&name_owned).map(|i| {
            if i.password.is_empty() {
                crate::config::Config::effective_default_password()
            } else {
                i.password.clone()
            }
        });
        drop(mgr); // 探测在锁外进行，避免长阻塞
        Ok(crate::instance::probe_free_completion(
            &name_owned,
            port,
            auth.as_deref(),
        ))
    })()
}

#[tauri::command]
pub async fn test_instance(
    state: tauri::State<'_, AppState>,
    name: String,
) -> Result<crate::instance::TestResult, String> {
    let core = state.core.clone();
    tokio::task::spawn_blocking(move || test_instance_core(&core, &name))
        .await
        .map_err(|e| format!("测试实例任务失败: {}", e))?
}
// ======================== 批量操作 ========================

pub(crate) fn sanitize_instance_name(node: &str) -> String {
    let s: String = node
        .chars()
        .map(|c| match c {
            '/' | '\\' | ':' | '*' | '?' | '"' | '<' | '>' | '|' => '-',
            c if c.is_control() => '-',
            c => c,
        })
        .collect();
    let s = s.trim().trim_matches('.').to_string();
    if s.is_empty() {
        "node".into()
    } else if s.chars().count() > 40 {
        s.chars().take(40).collect()
    } else {
        s
    }
}

pub fn batch_add_core(
    core: &AppCore,
    nodes: Vec<BatchAddItem>,
    base_port: Option<u16>,
    use_node_name: Option<bool>,
    name_prefix: Option<String>,
) -> Result<BatchAddResult, String> {
    if nodes.is_empty() {
        return Err("nodes 不能为空".to_string());
    }
    let base_port = base_port.unwrap_or(if cfg!(debug_assertions) { 30000 } else { 18100 });
    let use_node_name = use_node_name.unwrap_or(true);
    let prefix = name_prefix.unwrap_or_else(|| "n".to_string());

    let mut mgr = core.manager.lock().map_err(|_| "状态锁失败".to_string())?;
    let _ = mgr.load();
    Ok(batch_add_inner(
        &mut mgr,
        &nodes,
        base_port,
        use_node_name,
        &prefix,
    ))
}

/// batch_add 核心逻辑（独立纯函数便于单元测试）：
/// 按节点去重——同一节点只允许例化一次（已在实例列表中的节点跳过）。
fn batch_add_inner(
    mgr: &mut InstanceManager,
    nodes: &[BatchAddItem],
    base_port: u16,
    use_node_name: bool,
    prefix: &str,
) -> BatchAddResult {
    let mut added = Vec::new();
    let mut errors = Vec::new();

    for (i, item) in nodes.iter().enumerate() {
        let node = item.node.trim();
        if node.is_empty() {
            errors.push(json!({ "node": "", "error": "空节点名" }));
            continue;
        }
        // 按节点去重：同一节点只允许例化一次（已在实例列表中的节点跳过）
        if mgr.list_instances().iter().any(|x| x.node == node) {
            errors.push(json!({ "node": node, "error": "该节点已添加为实例" }));
            continue;
        }
        let port = item.port.unwrap_or(base_port.saturating_add(i as u16));
        let name = item
            .name
            .clone()
            .filter(|s| !s.trim().is_empty())
            .unwrap_or_else(|| {
                if use_node_name {
                    sanitize_instance_name(node)
                } else {
                    format!("{}{}", prefix, i + 1)
                }
            });

        // 名称冲突时自动加后缀
        let mut final_name = name.clone();
        let mut suffix = 2u32;
        while mgr.list_instances().iter().any(|x| x.name == final_name) {
            final_name = format!("{}-{}", name, suffix);
            suffix += 1;
            if suffix > 100 {
                break;
            }
        }

        // 端口冲突时递增
        let mut final_port = port;
        let mut tries = 0u16;
        while mgr.list_instances().iter().any(|x| x.port == final_port) {
            final_port = final_port.saturating_add(1);
            tries += 1;
            if tries > 200 {
                break;
            }
        }

        let ip = node_ip(node);
        let sk = gen_sk_key();
        match mgr.add_instance(final_name.clone(), final_port, node.to_string(), sk, ip) {
            Ok(()) => {
                added.push(json!({
                    "name": final_name,
                    "port": final_port,
                    "node": node,
                }));
            }
            Err(e) => {
                errors.push(json!({ "node": node, "error": e.to_string() }));
            }
        }
    }

    let added_count = added.len();
    let error_count = errors.len();
    BatchAddResult {
        added,
        errors,
        added_count,
        error_count,
    }
}

#[tauri::command]
pub fn batch_add(
    state: tauri::State<'_, AppState>,
    nodes: Vec<BatchAddItem>,
    base_port: Option<u16>,
    use_node_name: Option<bool>,
    name_prefix: Option<String>,
) -> Result<BatchAddResult, String> {
    batch_add_core(&state.core, nodes, base_port, use_node_name, name_prefix)
}

/// 对一批实例执行同一操作，汇总成功与失败明细。
fn batch_op_response(
    names: Vec<String>,
    mut op: impl FnMut(&str) -> anyhow::Result<()>,
) -> BatchOpResult {
    let mut success = Vec::new();
    let mut errors = serde_json::Map::new();
    for name in names {
        match op(&name) {
            Ok(()) => success.push(name),
            Err(e) => {
                errors.insert(name, json!(e.to_string()));
            }
        }
    }
    BatchOpResult {
        success_count: success.len(),
        error_count: errors.len(),
        success,
        errors,
    }
}

// 并行启动 worker（4 并发）：用临时 manager 启动，不持有共享锁
fn run_parallel_start_jobs(
    jobs: Vec<Instance>,
    binary_dir: PathBuf,
    runtime_dir: PathBuf,
) -> Vec<(String, std::result::Result<Instance, String>)> {
    if jobs.is_empty() {
        return Vec::new();
    }
    let worker_count = jobs.len().min(4);
    let queue = Arc::new(std::sync::Mutex::new(VecDeque::from_iter(jobs)));
    let results = Arc::new(std::sync::Mutex::new(Vec::new()));
    thread::scope(|scope| {
        for _ in 0..worker_count {
            let queue = Arc::clone(&queue);
            let results = Arc::clone(&results);
            let binary_dir = binary_dir.clone();
            let runtime_dir = runtime_dir.clone();
            scope.spawn(move || {
                loop {
                    let job = queue.lock().ok().and_then(|mut queue| queue.pop_front());
                    let Some(instance) = job else { break };
                    let name = instance.name.clone();
                    let result = crate::instance::start_instance_process(
                        instance,
                        &binary_dir,
                        &runtime_dir,
                    );
                    if let Ok(mut results) = results.lock() {
                        results.push((name, result));
                    }
                }
            });
        }
    });
    results
        .lock()
        .map(|results| results.clone())
        .unwrap_or_default()
}

// 并行停止 worker（8 并发）：直接按 PID 杀进程
fn run_parallel_stop_jobs(jobs: Vec<(String, Option<u32>, Option<u32>)>) -> Vec<String> {
    if jobs.is_empty() {
        return Vec::new();
    }
    let worker_count = jobs.len().min(8);
    let queue = Arc::new(std::sync::Mutex::new(VecDeque::from_iter(jobs)));
    let completed = Arc::new(std::sync::Mutex::new(Vec::new()));
    thread::scope(|scope| {
        for _ in 0..worker_count {
            let queue = Arc::clone(&queue);
            let completed = Arc::clone(&completed);
            scope.spawn(move || {
                loop {
                    let job = queue.lock().ok().and_then(|mut queue| queue.pop_front());
                    let Some((name, pid, singbox_pid)) = job else {
                        break;
                    };
                    if let Some(pid) = pid {
                        let _ = crate::instance::kill_process(pid);
                    }
                    if let Some(pid) = singbox_pid {
                        let _ = crate::instance::kill_process(pid);
                    }
                    if let Ok(mut completed) = completed.lock() {
                        completed.push(name);
                    }
                }
            });
        }
    });
    completed
        .lock()
        .map(|completed| completed.clone())
        .unwrap_or_default()
}

pub fn batch_start_core(core: &AppCore, names: Vec<String>) -> Result<BatchOpResult, String> {
    let manager = Arc::clone(&core.manager);
    let gateway = Arc::clone(&core.gateway);
    (|| {
        let mut unique_names = Vec::new();
        let mut seen = HashSet::new();
        for name in names {
            if seen.insert(name.clone()) {
                unique_names.push(name);
            }
        }

        // 短锁：标记全部实例为 Starting
        let (jobs, mut errors, binary_dir, runtime_dir) = {
            let mut mgr = manager.lock().map_err(|_| "状态锁失败".to_string())?;
            let _ = mgr.load();
            let mut jobs = Vec::new();
            let mut errs = serde_json::Map::new();
            for name in unique_names {
                match mgr.mark_starting(&name) {
                    Ok(instance) => jobs.push(instance),
                    Err(error) => {
                        errs.insert(name, json!(error.to_string()));
                    }
                }
            }
            mgr.save_state().map_err(|error| error.to_string())?;
            (jobs, errs, mgr.binary_dir.clone(), mgr.runtime_dir.clone())
        };

        // 放锁：并行启动（4 worker）
        let outcomes = run_parallel_start_jobs(jobs, binary_dir, runtime_dir);

        // 短锁：回写结果
        let mut success = Vec::new();
        {
            let mut mgr = manager.lock().map_err(|_| "状态锁失败".to_string())?;
            for (name, outcome) in outcomes {
                match outcome {
                    Ok(instance) => match mgr.apply_start_result(&name, Ok(instance)) {
                        Ok(()) => success.push(name),
                        Err(error) => {
                            errors.insert(name, json!(error.to_string()));
                        }
                    },
                    Err(error) => {
                        let _ = mgr.apply_start_result(&name, Err(error.clone()));
                        errors.insert(name, json!(error));
                    }
                }
            }
            mgr.save_state().map_err(|error| error.to_string())?;
        }

        let result = Ok(BatchOpResult {
            success_count: success.len(),
            error_count: errors.len(),
            success,
            errors,
        });

        // 同步网关（池成员可能变化）
        // 锁序统一 manager→gateway：先取快照放锁，再锁 gateway，避免与
        // start/stop_instance 的 manager→gateway 顺序相反导致死锁。
        let instances = manager
            .lock()
            .map(|mut mgr| {
                let _ = mgr.reconcile_states();
                mgr.list_instances().to_vec()
            })
            .unwrap_or_default();
        if let Ok(mut g) = gateway.lock() {
            let _ = g.sync(&instances);
        }

        result
    })()
}

#[tauri::command]
pub async fn batch_start(
    state: tauri::State<'_, AppState>,
    names: Vec<String>,
) -> Result<BatchOpResult, String> {
    let core = state.core.clone();
    tokio::task::spawn_blocking(move || batch_start_core(&core, names))
        .await
        .map_err(|e| format!("批量启动任务失败: {}", e))?
}

pub fn batch_stop_core(core: &AppCore, names: Vec<String>) -> Result<BatchOpResult, String> {
    let manager = Arc::clone(&core.manager);
    let gateway = Arc::clone(&core.gateway);
    (|| {
        let mut unique_names = Vec::new();
        let mut seen = HashSet::new();
        for name in names {
            if seen.insert(name.clone()) {
                unique_names.push(name);
            }
        }

        // 短锁：标记全部实例为 Stopping，取出 PID
        let (jobs, mut errors) = {
            let mut mgr = manager.lock().map_err(|_| "状态锁失败".to_string())?;
            let _ = mgr.load();
            let mut jobs = Vec::new();
            let mut errs = serde_json::Map::new();
            for name in unique_names {
                match mgr.prepare_stop(&name) {
                    Ok((pid, singbox_pid)) => jobs.push((name, pid, singbox_pid)),
                    Err(error) => {
                        errs.insert(name, json!(error.to_string()));
                    }
                }
            }
            mgr.save_state().map_err(|error| error.to_string())?;
            (jobs, errs)
        };

        // 放锁：并行杀进程（8 worker）
        let completed = run_parallel_stop_jobs(jobs);

        // 短锁：回写停止状态
        let mut success = Vec::new();
        {
            let mut mgr = manager.lock().map_err(|_| "状态锁失败".to_string())?;
            for name in completed {
                match mgr.finish_stop(&name) {
                    Ok(()) => success.push(name),
                    Err(error) => {
                        errors.insert(name, json!(error.to_string()));
                    }
                }
            }
            mgr.save_state().map_err(|error| error.to_string())?;
        }
        let result = Ok(BatchOpResult {
            success_count: success.len(),
            error_count: errors.len(),
            success,
            errors,
        });

        // 同步网关（池成员可能变化）
        // 锁序统一 manager→gateway（见 batch_start_core 注释）
        let instances = manager
            .lock()
            .map(|mut mgr| {
                let _ = mgr.reconcile_states();
                mgr.list_instances().to_vec()
            })
            .unwrap_or_default();
        if let Ok(mut g) = gateway.lock() {
            let _ = g.sync(&instances);
        }

        result
    })()
}

#[tauri::command]
pub async fn batch_stop(
    state: tauri::State<'_, AppState>,
    names: Vec<String>,
) -> Result<BatchOpResult, String> {
    let core = state.core.clone();
    tokio::task::spawn_blocking(move || batch_stop_core(&core, names))
        .await
        .map_err(|e| format!("批量停止任务失败: {}", e))?
}

// ======================== 一键重启（含总端口强制清理） ========================

/// 查找占用指定端口的进程 PID（Windows 用 netstat 解析 LISTENING 行）。
#[cfg(windows)]
fn pids_on_port(port: u16) -> Vec<u32> {
    let mut pids = Vec::new();
    let Ok(out) = std::process::Command::new("netstat")
        .args(["-ano", "-p", "tcp"])
        .output()
    else {
        return pids;
    };
    let text = String::from_utf8_lossy(&out.stdout);
    let needle = format!(":{}", port);
    for line in text.lines() {
        if !line.contains(&needle) {
            continue;
        }
        let parts: Vec<&str> = line.split_whitespace().collect();
        if parts.len() < 5 {
            continue;
        }
        // netstat 行：Proto LocalAddr ForeignAddr State PID
        let state = parts[3];
        if !(state == "LISTENING" || state == "ESTABLISHED" || state == "TIME_WAIT") {
            continue;
        }
        if let Ok(pid) = parts[4].parse::<u32>() {
            if pid != 0 && !pids.contains(&pid) {
                pids.push(pid);
            }
        }
    }
    pids
}

#[cfg(not(windows))]
fn pids_on_port(_port: u16) -> Vec<u32> {
    Vec::new()
}

/// 强制释放端口：找到占用者并 taskkill，返回成功杀掉的进程 PID 列表。
fn force_free_port(port: u16) -> Vec<u32> {
    let mut freed = Vec::new();
    let pids = pids_on_port(port);
    for pid in pids {
        if crate::instance::kill_process(pid).is_ok() {
            freed.push(pid);
        }
    }
    freed
}

/// 一键重启实例池（含统一网关总端口）：
/// 1. 停止统一网关（总端口释放）
/// 2. 停止所有实例（含残留 pid 的僵尸进程）
/// 3. 强制清理所有池成员 singbox 端口 + 总端口（解决占用）
/// 4. 并行启动全部池成员
/// 5. 同步网关（自动拉起总端口）
pub fn restart_pool_core(core: &AppCore) -> Result<RestartPoolResult, String> {
    let manager = Arc::clone(&core.manager);
    let gateway = Arc::clone(&core.gateway);
    (|| {
        // 1) 停统一网关
        if let Ok(mut g) = gateway.lock() {
            g.stop();
        }

        // 2) 全停实例（含残留 pid 的僵尸进程）
        {
            let Ok(mut mgr) = manager.lock() else {
                return Err("状态锁失败".to_string());
            };
            let _ = mgr.load();
            let names: Vec<String> = mgr
                .list_instances()
                .iter()
                .filter(|i| {
                    i.pid.is_some()
                        || i.singbox_pid.is_some()
                        || i.status == crate::instance::InstanceStatus::Running
                        || i.status == crate::instance::InstanceStatus::Starting
                })
                .map(|i| i.name.clone())
                .collect();
            for n in names {
                let _ = mgr.stop_instance(&n);
            }
        }
        std::thread::sleep(std::time::Duration::from_millis(300));

        // 3) 收集池成员端口 + 总端口，强制清理
        let (pool_names, member_ports) = {
            let mut mgr = manager.lock().map_err(|_| "状态锁失败".to_string())?;
            let _ = mgr.load();
            let names: Vec<String> = mgr
                .list_instances()
                .iter()
                .filter(|i| i.join_gateway)
                .map(|i| i.name.clone())
                .collect();
            let ports: Vec<u16> = mgr
                .list_instances()
                .iter()
                .filter(|i| i.join_gateway)
                .map(|i| i.singbox_port)
                .collect();
            (names, ports)
        };

        let mut freed_ports: Vec<u16> = Vec::new();
        let mut all_ports = member_ports.clone();
        all_ports.push(crate::gateway::UNIFIED_GATEWAY_PORT);
        for port in all_ports {
            // 端口仍被占则强清
            if !crate::instance::is_port_free(port) {
                let freed = force_free_port(port);
                if !freed.is_empty() {
                    freed_ports.push(port);
                }
            }
        }
        // 再等一拍让端口真正释放
        std::thread::sleep(std::time::Duration::from_millis(200));

        // 4) 并行启动全部池成员
        let started = if pool_names.is_empty() {
            0
        } else {
            let mut mgr = manager.lock().map_err(|_| "状态锁失败".to_string())?;
            let _ = mgr.load();
            let mut jobs = Vec::new();
            for name in &pool_names {
                if let Ok(instance) = mgr.mark_starting(name) {
                    jobs.push(instance);
                }
            }
            let (binary_dir, runtime_dir) = (mgr.binary_dir.clone(), mgr.runtime_dir.clone());
            mgr.save_state().ok();
            drop(mgr);

            let outcomes = run_parallel_start_jobs(jobs, binary_dir, runtime_dir);
            let mut success = 0usize;
            if let Ok(mut mgr) = manager.lock() {
                for (name, outcome) in outcomes {
                    match outcome {
                        Ok(instance) => {
                            if mgr.apply_start_result(&name, Ok(instance)).is_ok() {
                                success += 1;
                            }
                        }
                        Err(error) => {
                            let _ = mgr.apply_start_result(&name, Err(error.clone()));
                        }
                    }
                }
                mgr.save_state().ok();
            }
            success
        };

        // 5) 同步网关（自动拉起总端口）
        let mut gateway_running = false;
        let mut error = None;
        // 锁序统一 manager→gateway（先取快照放锁，再锁 gateway，避免死锁）
        let instances = manager
            .lock()
            .map(|mut mgr| {
                let _ = mgr.reconcile_states();
                mgr.list_instances().to_vec()
            })
            .unwrap_or_default();
        if let Ok(mut g) = gateway.lock() {
            let total = instances.len();
            match g.sync(&instances) {
                Ok(()) => {
                    // status() 会在池非空且网关未启动时自动拉起，并返回运行态
                    gateway_running = g.status(total).running;
                }
                Err(e) => error = Some(e.to_string()),
            }
        }

        Ok(RestartPoolResult {
            stopped: pool_names.len(),
            started,
            freed_ports,
            gateway_running,
            error,
        })
    })()
}

#[tauri::command]
pub async fn restart_pool(state: tauri::State<'_, AppState>) -> Result<RestartPoolResult, String> {
    let core = state.core.clone();
    tokio::task::spawn_blocking(move || restart_pool_core(&core))
        .await
        .map_err(|e| format!("重启实例池任务失败: {}", e))?
}

pub fn batch_delete_core(core: &AppCore, names: Vec<String>) -> Result<BatchOpResult, String> {
    let manager = Arc::clone(&core.manager);
    let gateway = Arc::clone(&core.gateway);
    (|| {
        let mut mgr = manager.lock().map_err(|_| "状态锁失败".to_string())?;
        let _ = mgr.load();
        let result = batch_op_response(names, |name| mgr.remove_instance(name));
        let _ = mgr.save_state();
        let instances = mgr.list_instances().to_vec();
        drop(mgr);
        // 同步网关：删除的实例若在池中，需从代理池剔除
        if let Ok(mut g) = gateway.lock() {
            let _ = g.sync(&instances);
        }
        Ok(result)
    })()
}

#[tauri::command]
pub async fn batch_delete(
    state: tauri::State<'_, AppState>,
    names: Vec<String>,
) -> Result<BatchOpResult, String> {
    let core = state.core.clone();
    tokio::task::spawn_blocking(move || batch_delete_core(&core, names))
        .await
        .map_err(|e| format!("批量删除任务失败: {}", e))?
}

// ======================== 端口建议 / 校验 ========================

fn is_port_used(mgr: &InstanceManager, port: u16) -> bool {
    if mgr.list_instances().iter().any(|i| i.port == port) {
        return true;
    }
    !crate::instance::is_port_free(port)
}

fn rand_seed() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos() as u64)
        .unwrap_or(0x1234_5678)
}

/// 建议一个可用端口（未被实例占用，本地未监听）
/// 调试构建（tauri dev）使用 30000+ 段，与正式版（10000-29999 段）完全错开，
/// 避免两套实例的 API/sing-box 端口（port 与 port+10000）冲突。
pub fn port_suggest_core(core: &AppCore) -> Result<u16, String> {
    let manager = Arc::clone(&core.manager);
    (|| {
        let mgr = manager.lock().map_err(|_| "状态锁失败".to_string())?;
        let mut rng = rand_seed();
        // debug 构建：30000+ 段（实际返回 30001-30199）；release：保持 main 原公式（18100 起，分布同 main）
        let (base, range): (u16, u16) = if cfg!(debug_assertions) {
            (30000, 200)
        } else {
            (18100, 20000)
        };
        let start = base + (rng % range as u64) as u16;
        for _ in 0..200 {
            let port = if cfg!(debug_assertions) {
                start.saturating_add(1 + (rng % 200) as u16) % range + base
            } else {
                // 与 main 完全一致：% 30000 + 10000（建议范围 [10000, 39999]）
                start.saturating_add(1 + (rng % 200) as u16) % 30000 + 10000
            };
            if !is_port_used(&mgr, port) {
                return Ok(port);
            }
            rng = rng
                .wrapping_mul(6364136223846793005)
                .wrapping_add(1442695040888963407);
        }
        Err("未找到可用端口".to_string())
    })()
}

#[tauri::command]
pub async fn port_suggest(state: tauri::State<'_, AppState>) -> Result<u16, String> {
    let core = state.core.clone();
    tokio::task::spawn_blocking(move || port_suggest_core(&core))
        .await
        .map_err(|e| format!("端口建议任务失败: {}", e))?
}

pub fn port_check_core(core: &AppCore, port: u16) -> Result<PortCheckResult, String> {
    if port < 1024 {
        return Err("端口需 >= 1024".to_string());
    }
    let manager = Arc::clone(&core.manager);
    (|| {
        let mut mgr = manager.lock().map_err(|_| "状态锁失败".to_string())?;
        let _ = mgr.load();
        if mgr.list_instances().iter().any(|i| i.port == port) {
            return Ok(PortCheckResult {
                available: false,
                reason: "已被实例占用".to_string(),
            });
        }
        if !crate::instance::is_port_free(port) {
            return Ok(PortCheckResult {
                available: false,
                reason: "端口已被本机程序监听".to_string(),
            });
        }
        Ok(PortCheckResult {
            available: true,
            reason: "端口可用".to_string(),
        })
    })()
}

#[tauri::command]
pub async fn port_check(
    state: tauri::State<'_, AppState>,
    port: u16,
) -> Result<PortCheckResult, String> {
    let core = state.core.clone();
    tokio::task::spawn_blocking(move || port_check_core(&core, port))
        .await
        .map_err(|e| format!("端口检查任务失败: {}", e))?
}
// ======================== 节点扫描 ========================

#[derive(Debug, Default, Clone)]
pub struct ScanStartOpts {
    pub nodes: Option<Vec<String>>,
    pub api_port: Option<u16>,
    pub socks_port: Option<u16>,
    pub timeout: Option<u64>,
    pub concurrency: Option<usize>,
}

pub fn scan_start_core(
    core: &AppCore,
    opts: ScanStartOpts,
) -> Result<crate::probe::ScanProgress, String> {
    let (_, binary_dir, runtime_dir) = manager_paths();
    let password = default_password();
    let api_port = opts.api_port.unwrap_or(DEFAULT_PROBE_API_PORT);
    let socks_port = opts.socks_port.unwrap_or(DEFAULT_PROBE_SOCKS_PORT);
    let timeout = opts.timeout.unwrap_or(25);
    let filter = opts.nodes.filter(|v| !v.is_empty());
    let concurrency = opts.concurrency;

    match core.scan.start_scan(
        binary_dir,
        runtime_dir,
        password,
        api_port,
        socks_port,
        filter,
        timeout,
        concurrency,
    ) {
        Ok(()) => Ok(core.scan.progress_snapshot()),
        Err(e) => Err(e.to_string()),
    }
}

#[tauri::command]
pub fn scan_start(
    state: tauri::State<'_, AppState>,
    nodes: Option<Vec<String>>,
    api_port: Option<u16>,
    socks_port: Option<u16>,
    timeout: Option<u64>,
    concurrency: Option<usize>,
) -> Result<crate::probe::ScanProgress, String> {
    scan_start_core(
        &state.core,
        ScanStartOpts {
            nodes,
            api_port,
            socks_port,
            timeout,
            concurrency,
        },
    )
}

pub fn scan_status_core(core: &AppCore) -> Result<crate::probe::ScanProgress, String> {
    Ok(core.scan.progress_snapshot())
}

#[tauri::command]
pub fn scan_status(
    state: tauri::State<'_, AppState>,
) -> Result<crate::probe::ScanProgress, String> {
    scan_status_core(&state.core)
}

pub fn scan_stop_core(core: &AppCore) -> Result<crate::probe::ScanProgress, String> {
    core.scan.request_stop();
    Ok(core.scan.progress_snapshot())
}

#[tauri::command]
pub fn scan_stop(state: tauri::State<'_, AppState>) -> Result<crate::probe::ScanProgress, String> {
    scan_stop_core(&state.core)
}

// ======================== 订阅拉取 ========================

/// 拉取订阅并返回节点预览（不落库）
pub fn subscribe_preview_core(url: &str) -> Result<Vec<crate::subscribe::SubscribeNode>, String> {
    crate::subscribe::fetch_subscription(url)
}

#[tauri::command]
pub fn subscribe_preview(url: String) -> Result<Vec<crate::subscribe::SubscribeNode>, String> {
    subscribe_preview_core(&url)
}

/// 拉取订阅并批量导入为实例（同时持久化订阅缓存）。
/// `join_gateway` 为 true 时导入的实例打上入池标记（不自动启动）。
pub fn subscribe_import_core(
    core: &AppCore,
    url: &str,
    join_gateway: bool,
) -> Result<usize, String> {
    crate::subscribe::import_subscription(core, url, join_gateway)
}

#[tauri::command]
pub fn subscribe_import(
    state: tauri::State<'_, AppState>,
    url: String,
    join_gateway: bool,
) -> Result<usize, String> {
    subscribe_import_core(&state.core, &url, join_gateway)
}

/// 仅拉取并缓存订阅节点（不创建实例），供节点池页「从订阅导入」使用。
pub fn subscribe_import_pool_core(url: &str) -> Result<usize, String> {
    crate::subscribe::import_subscription_pool(url)
}

#[tauri::command]
pub fn subscribe_import_pool(url: String) -> Result<usize, String> {
    subscribe_import_pool_core(&url)
}

// ======================== 健康巡检 ========================

/// 立即执行一轮健康巡检
pub fn health_check_now_core(core: &AppCore) -> crate::health::HealthSummary {
    crate::health::run_health_check_once(core)
}

#[tauri::command]
pub fn health_check_now(state: tauri::State<'_, AppState>) -> crate::health::HealthSummary {
    health_check_now_core(&state.core)
}

/// 读取最近一次巡检汇总（立即执行一轮）
pub fn health_summary_core(core: &AppCore) -> crate::health::HealthSummary {
    crate::health::run_health_check_once(core)
}

#[tauri::command]
pub fn health_summary(state: tauri::State<'_, AppState>) -> crate::health::HealthSummary {
    health_summary_core(&state.core)
}

// ======================== 配置 ========================

pub fn config_get_core() -> Result<ConfigView, String> {
    let cfg = Config::load().unwrap_or_default();
    Ok(ConfigView {
        base_url: cfg.base_url,
        default_password: if cfg.default_password.is_empty() {
            "".to_string()
        } else {
            "***".to_string()
        },
        has_password: !cfg.default_password.is_empty(),
        clash_external_url: cfg.clash_external_url,
        has_clash_token: !cfg.clash_auth_token.is_empty(),
        timeout_ttft_min_ms: cfg.timeout_ttft_min_ms.unwrap_or(10000),
        timeout_ttft_max_ms: cfg.timeout_ttft_max_ms.unwrap_or(10000),
        timeout_silence_min_ms: cfg.timeout_silence_min_ms.unwrap_or(5000),
        timeout_silence_max_ms: cfg.timeout_silence_max_ms.unwrap_or(5000),
        failover_probe_min: cfg.failover_probe_min.unwrap_or(2),
        failover_probe_max: cfg.failover_probe_max.unwrap_or(3),
        call_log_max: cfg.call_log_max.unwrap_or(5000),
        show_node_prefix: cfg.show_node_prefix.unwrap_or(false),
        gateway_port: Config::effective_gateway_port(),
        gateway_key: if cfg.gateway_key.as_deref().unwrap_or("").is_empty() {
            "".to_string()
        } else {
            "***".to_string()
        },
        has_gateway_key: !cfg.gateway_key.as_deref().unwrap_or("").is_empty(),
        http_port: Config::effective_http_port(),
        subscribe_url: cfg.subscribe_url.unwrap_or_default(),
        subscribe_interval_min: cfg.subscribe_interval_min.unwrap_or(0),
        health_check_interval_sec: cfg.health_check_interval_sec.unwrap_or(0),
        health_restart_threshold: cfg.health_restart_threshold.unwrap_or(0),
        log_filter_keywords: cfg.log_filter_keywords.unwrap_or_default(),
    })
}

#[tauri::command]
pub fn config_get() -> Result<ConfigView, String> {
    config_get_core()
}

pub fn config_set_core(core: &AppCore, key: &str, value: &str) -> Result<(), String> {
    let mut cfg = Config::load().unwrap_or_default();
    cfg.set(key, value).map_err(|e| e.to_string())?;

    // 关键配置写入后，重新生成网关配置并让 Go 端热加载（无需重启网关）。
    // 涉及网关行为的配置：前缀开关、路由超时区间、探测数、日志上限。
    let gateway_related = matches!(
        key,
        "show_node_prefix"
            | "timeout_ttft_min_ms"
            | "timeout_ttft_max_ms"
            | "timeout_silence_min_ms"
            | "timeout_silence_max_ms"
            | "failover_probe_min"
            | "failover_probe_max"
            | "call_log_max"
            | "route_mode"
    );
    if gateway_related {
        // 先取实例快照并释放 manager 锁，再单独锁 gateway，避免与
        // batch_start/batch_stop 中 gateway→manager 的加锁顺序相反导致死锁。
        let instances = core
            .manager
            .lock()
            .map(|mut mgr| {
                let _ = mgr.load();
                mgr.list_instances().to_vec()
            })
            .unwrap_or_default();
        if let Ok(mut g) = core.gateway.lock() {
            let _ = g.sync(&instances);
        }
    }
    // 网关端口/密钥变更：运行中的 Go 进程不会重读，需重建网关使新配置生效
    if matches!(key, "gateway_port" | "gateway_key") {
        let instances = core
            .manager
            .lock()
            .map(|mut mgr| {
                let _ = mgr.load();
                mgr.list_instances().to_vec()
            })
            .unwrap_or_default();
        if let Ok(mut g) = core.gateway.lock() {
            g.stop();
            let _ = g.sync(&instances);
        }
    }
    Ok(())
}

#[tauri::command]
pub fn config_set(
    state: tauri::State<'_, AppState>,
    key: String,
    value: String,
) -> Result<(), String> {
    config_set_core(&state.core, &key, &value)
}

// ======================== 开机自启（Windows 注册表） ========================

#[cfg(windows)]
const RUN_KEY: &str = r"HKCU\Software\Microsoft\Windows\CurrentVersion\Run";
#[cfg(windows)]
const RUN_NAME: &str = "opencode2api-manager";

#[cfg(windows)]
fn autostart_status() -> anyhow::Result<bool> {
    let out = no_window(&mut std::process::Command::new("reg"))
        .args(["query", RUN_KEY, "/v", RUN_NAME])
        .output()?;
    Ok(out.status.success())
}

#[cfg(not(windows))]
fn autostart_status() -> anyhow::Result<bool> {
    // 非 Windows 平台不支持开机自启：读取状态返回「未启用」（合理状态），
    // 仅 SET 时报错（autostart_set_impl），避免设置页加载因 GET 失败整体崩溃
    Ok(false)
}

#[cfg(windows)]
fn set_autostart(enabled: bool) -> anyhow::Result<()> {
    if enabled {
        let exe = std::env::current_exe().unwrap_or_default();
        let val = format!("\"{}\"", exe.display());
        no_window(&mut std::process::Command::new("reg"))
            .args([
                "add", RUN_KEY, "/v", RUN_NAME, "/t", "REG_SZ", "/d", &val, "/f",
            ])
            .output()?;
    } else {
        // 幂等：值不存在时删除失败也可接受
        let _ = no_window(&mut std::process::Command::new("reg"))
            .args(["delete", RUN_KEY, "/v", RUN_NAME, "/f"])
            .output();
    }
    Ok(())
}

#[cfg(not(windows))]
fn autostart_set_impl(_enabled: bool) -> anyhow::Result<()> {
    anyhow::bail!("仅 Windows 支持开机自启")
}

pub fn autostart_get_core() -> Result<bool, String> {
    autostart_status().map_err(|e| e.to_string())
}

#[tauri::command]
pub fn autostart_get() -> Result<bool, String> {
    autostart_get_core()
}

pub fn autostart_set_core(enabled: bool) -> Result<(), String> {
    #[cfg(windows)]
    {
        set_autostart(enabled).map_err(|e| e.to_string())
    }
    #[cfg(not(windows))]
    {
        autostart_set_impl(enabled).map_err(|e| e.to_string())
    }
}

#[tauri::command]
pub fn autostart_set(enabled: bool) -> Result<(), String> {
    autostart_set_core(enabled)
}

// ======================== 二进制信息 ========================

pub fn get_binaries_info_core() -> BinariesInfo {
    let (_, binary_dir, _) = manager_paths();
    let platform = if cfg!(windows) {
        "windows"
    } else if cfg!(target_os = "linux") {
        "linux"
    } else {
        "macos"
    };
    BinariesInfo {
        bin_dir: binary_dir.display().to_string(),
        oc_exists: binary_dir.join("opencode2api.exe").exists()
            || binary_dir.join("opencode2api").exists(),
        sb_exists: binary_dir.join("sing-box.exe").exists() || binary_dir.join("sing-box").exists(),
        platform: platform.to_string(),
    }
}

#[tauri::command]
pub fn get_binaries_info() -> BinariesInfo {
    get_binaries_info_core()
}

// ======================== 窗口控制（托盘） ========================

/// 返回本地管理 HTTP 服务实际端口（桌面前端据此构造 API 地址；
/// http_port 可配置，前端不能写死 19090）。
#[tauri::command]
pub fn get_http_port() -> u16 {
    crate::config::Config::effective_http_port()
}

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

// ======================== Token 统计（按实例） ========================

/// 单个模型维度的统计（来自 Go 核心 stats.json 的 models 项）
#[derive(Debug, Serialize)]
pub struct ModelStat {
    pub model: String,
    pub requests: i64,
    pub prompt_tokens: i64,
    pub completion_tokens: i64,
    pub total_tokens: i64,
}

/// 统一网关下单个节点（实例出口）的统计明细（来自 Go 核心 node_stats.json）
#[derive(Debug, Serialize)]
pub struct GatewayNodeStat {
    pub name: String,
    pub addr: String,
    pub requests: i64,
    pub prompt_tokens: i64,
    pub completion_tokens: i64,
    pub total_tokens: i64,
}

/// 单个实例的统计汇总（按实例目录聚合 stats.json）
#[derive(Debug, Serialize)]
pub struct InstanceStat {
    pub name: String,
    /// 目录存在但实例列表中没有（已删除/历史实例）时为 false
    pub exists: bool,
    pub requests: i64,
    pub prompt_tokens: i64,
    pub completion_tokens: i64,
    pub total_tokens: i64,
    pub models: Vec<ModelStat>,
    /// 仅统一网关条目：按节点（SOCKS5 出口）拆分的调用统计
    #[serde(default)]
    pub nodes: Vec<GatewayNodeStat>,
}

/// 全局统计总览（全部实例求和）
#[derive(Debug, Serialize)]
pub struct StatsSummary {
    pub total_requests: i64,
    pub total_prompt_tokens: i64,
    pub total_completion_tokens: i64,
    pub total_tokens: i64,
    pub instances: Vec<InstanceStat>,
}

/// Go 核心 stats.json 的解析结构（字段名与 Go 侧 JSON 一致）
#[derive(Debug, Deserialize)]
struct GoModelStats {
    #[serde(default)]
    request_count: i64,
    #[serde(default)]
    prompt_tokens: i64,
    #[serde(default)]
    completion_tokens: i64,
    #[serde(default)]
    total_tokens: i64,
}

#[derive(Debug, Deserialize)]
struct GoStatsData {
    // 只取 models 汇总：Go 侧 total_requests 与 sum(models.request_count) 等价，
    // 用 models 求和保证实例内一致性；多余字段由 serde 自动忽略
    #[serde(default)]
    models: std::collections::HashMap<String, GoModelStats>,
}

/// Go 核心 node_stats.json（统一网关按节点统计）的解析结构
#[derive(Debug, Deserialize)]
struct GoNodeStat {
    #[serde(default)]
    request_count: i64,
    #[serde(default)]
    prompt_tokens: i64,
    #[serde(default)]
    completion_tokens: i64,
    #[serde(default)]
    total_tokens: i64,
}

#[derive(Debug, Deserialize)]
struct GoNodeStatsData {
    #[serde(default)]
    nodes: std::collections::HashMap<String, GoNodeStat>,
}

/// 按实例读取 token 统计：扫描 runtime/ 下各实例目录的 stats.json，
/// 汇总出全局总览 + 实例列表（按总计 token 降序）。
/// - 实例目录存在但实例列表中没有 → exists=false（已删除，统计保留）
/// - stats.json 缺失或解析失败 → 跳过该目录，不报错
pub fn get_stats_core() -> Result<StatsSummary, String> {
    let (_, _, runtime_dir) = manager_paths();

    let manager = create_manager();
    let known_names: Vec<String> = manager
        .list_instances()
        .iter()
        .map(|i| i.name.clone())
        .collect();
    let port_to_name: std::collections::HashMap<u16, String> = manager
        .list_instances()
        .iter()
        .map(|i| (i.singbox_port, i.name.clone()))
        .collect();

    Ok(aggregate_stats(&runtime_dir, &known_names, &port_to_name))
}

#[tauri::command]
pub fn get_stats() -> Result<StatsSummary, String> {
    get_stats_core()
}

/// 读取统一网关全流程调用日志（call_log.jsonl），返回最新 N 条。
/// 文件位于 runtime/_unified-gateway/call_log.jsonl（Go 网关进程 cwd 决定）。
pub fn get_call_log_core(limit: Option<usize>) -> Vec<crate::call_log::CallLogRecord> {
    let (_, _, runtime_dir) = manager_paths();
    let path = runtime_dir.join("_unified-gateway").join("call_log.jsonl");
    let max = limit.unwrap_or(5000).clamp(1, 50000);
    crate::call_log::read_call_log(&path, max)
}

#[tauri::command]
pub fn get_call_log(limit: Option<usize>) -> Vec<crate::call_log::CallLogRecord> {
    get_call_log_core(limit)
}

/// 按过滤条件查询调用日志（read_call_log_filtered 的 core 入口）。
pub fn call_log_filtered_core(
    filter: &crate::call_log::CallLogFilter,
) -> Vec<crate::call_log::CallLogRecord> {
    let (_, _, runtime_dir) = manager_paths();
    let path = runtime_dir.join("_unified-gateway").join("call_log.jsonl");
    crate::call_log::read_call_log_filtered(&path, filter)
}

#[tauri::command]
pub fn call_log_filtered(
    filter: crate::call_log::CallLogFilter,
) -> Vec<crate::call_log::CallLogRecord> {
    call_log_filtered_core(&filter)
}

/// 日志聚合统计（call_log_aggregate 的 core 入口）。
pub fn call_log_aggregate_core() -> Vec<crate::call_log::CallLogAggregate> {
    let (_, _, runtime_dir) = manager_paths();
    let path = runtime_dir.join("_unified-gateway").join("call_log.jsonl");
    crate::call_log::call_log_aggregate(&path)
}

#[tauri::command]
pub fn call_log_aggregate() -> Vec<crate::call_log::CallLogAggregate> {
    call_log_aggregate_core()
}

/// 清空统一网关调用日志（删除 call_log.jsonl）。
/// Go 网关进程的内存环形缓冲不会把旧记录重新写回文件（Append 只追加新记录），
/// 因此删除文件后日志页即为空，后续新请求从空文件开始记录。
pub fn clear_call_log_core() -> Result<(), String> {
    let (_, _, runtime_dir) = manager_paths();
    let path = runtime_dir.join("_unified-gateway").join("call_log.jsonl");
    if path.exists() {
        std::fs::remove_file(&path).map_err(|e| format!("删除日志文件失败: {}", e))?;
    }
    Ok(())
}

#[tauri::command]
pub fn clear_call_log() -> Result<(), String> {
    clear_call_log_core()
}

/// 重置 Token 统计的结果汇总
#[derive(Debug, Serialize)]
pub struct ResetStatsResult {
    /// 成功重置的项数（含实例与统一网关）
    pub reset_count: usize,
    /// 清除的「已删除实例」历史统计目录数（勾选清除时）
    pub deleted_count: usize,
    /// 失败明细（每项一条）
    pub failed: Vec<String>,
}

/// 覆写为空统计文件（stats.json 或 node_stats.json，字段与 Go 侧一致）
fn write_empty_stats_file(path: &std::path::Path, is_nodes: bool) -> std::io::Result<()> {
    let empty = if is_nodes {
        serde_json::json!({ "total_requests": 0, "nodes": {} })
    } else {
        serde_json::json!({ "total_requests": 0, "models": {} })
    };
    std::fs::write(
        path,
        serde_json::to_string_pretty(&empty).unwrap_or_default(),
    )
}

/// 重置 Token 统计核心逻辑（纯函数，桌面 command 与 headless HTTP 共用）：
/// - 运行中的实例 / 统一网关：调用其 HTTP DELETE /api/reset-stats（Bearer 密钥，apiKeyAuth 门禁）
/// - 未运行的实例 / 网关：直接覆写磁盘 stats.json / node_stats.json 为空
/// 返回成功重置的项数与失败明细（单条失败不阻断整体）。
pub fn reset_stats_core(
    manager: Arc<Mutex<InstanceManager>>,
    clear_deleted: bool,
) -> Result<ResetStatsResult, String> {
    let (_, _, runtime_dir) = manager_paths();
    let default_pw = Config::effective_default_password();
    let mut reset_count = 0usize;
    let mut failed: Vec<String> = Vec::new();

    // 1) 实例：先校正状态，运行中走 HTTP，其余覆盖磁盘文件
    let instances = {
        let mut mgr = manager.lock().map_err(|_| "状态锁失败".to_string())?;
        mgr.load().ok();
        let _ = mgr.reconcile_states();
        mgr.list_instances().to_vec()
    };
    for inst in &instances {
        let stats_path = runtime_dir.join(&inst.name).join("stats.json");
        if inst.status == crate::instance::InstanceStatus::Running {
            let pw = if inst.password.is_empty() {
                default_pw.clone()
            } else {
                inst.password.clone()
            };
            match crate::instance::http_delete_json(
                inst.port,
                "/api/reset-stats",
                std::time::Duration::from_secs(6),
                Some(&pw),
            ) {
                Ok((status, _)) if (200..300).contains(&status) => reset_count += 1,
                Ok((status, _)) => failed.push(format!("{}: HTTP {}", inst.name, status)),
                Err(e) => failed.push(format!("{}: {}", inst.name, e)),
            }
        } else if stats_path.exists() {
            match write_empty_stats_file(&stats_path, false) {
                Ok(()) => reset_count += 1,
                Err(e) => failed.push(format!("{}: 覆写 stats.json 失败 ({})", inst.name, e)),
            }
        }
    }

    // 2) 统一网关：先尝试 HTTP；失败（未运行 / 旧二进制无该端点）则覆写磁盘文件
    let gw_dir = runtime_dir.join("_unified-gateway");
    let gw_reset_ok = crate::instance::http_delete_json(
        crate::config::Config::effective_gateway_port(),
        "/api/reset-stats",
        std::time::Duration::from_secs(6),
        Some(&crate::config::Config::effective_gateway_key()),
    )
    .map(|(status, _)| (200..300).contains(&status))
    .unwrap_or(false);
    if gw_reset_ok {
        reset_count += 1;
    } else {
        let mut any = false;
        for (fname, is_nodes) in [("stats.json", false), ("node_stats.json", true)] {
            let p = gw_dir.join(fname);
            if p.exists() && write_empty_stats_file(&p, is_nodes).is_ok() {
                any = true;
            }
        }
        if any {
            reset_count += 1;
        }
    }

    // 3) 清除「已删除实例」的历史目录（勾选时）：删除节点数据与节点本身。
    //    遍历 runtime/ 下名不在当前实例列表、非 _unified-gateway/_probe、
    //    且含统计文件（stats.json / node_stats.json）的目录，整目录删除。
    let mut deleted_count = 0usize;
    if clear_deleted {
        let known: HashSet<&String> = instances.iter().map(|i| &i.name).collect();
        if let Ok(entries) = std::fs::read_dir(&runtime_dir) {
            for entry in entries.flatten() {
                let dir = entry.path();
                if !dir.is_dir() {
                    continue;
                }
                let name = dir.file_name().and_then(|s| s.to_str()).unwrap_or("");
                if name == "_unified-gateway" || name == "_probe" {
                    continue;
                }
                if known.contains(&name.to_string()) {
                    continue;
                }
                // 仅处理真实含统计文件的历史实例目录
                let has_stats = ["stats.json", "node_stats.json"]
                    .iter()
                    .any(|f| dir.join(f).exists());
                if !has_stats {
                    continue;
                }
                match std::fs::remove_dir_all(&dir) {
                    Ok(()) => deleted_count += 1,
                    Err(e) => failed.push(format!("{}: 删除历史目录失败 ({})", name, e)),
                }
            }
        }
    }

    Ok(ResetStatsResult {
        reset_count,
        deleted_count,
        failed,
    })
}

/// Tauri command 壳：逻辑复用 reset_stats_core（headless 走 /api/stats/reset）。
#[tauri::command]
pub async fn reset_stats(
    state: tauri::State<'_, AppState>,
    clear_deleted: Option<bool>,
) -> Result<ResetStatsResult, String> {
    let clear_deleted = clear_deleted.unwrap_or(true);
    let manager = Arc::clone(&state.core.manager);
    tauri::async_runtime::spawn_blocking(move || reset_stats_core(manager, clear_deleted))
        .await
        .map_err(|e| e.to_string())?
}

/// 聚合逻辑（独立函数便于单元测试）：遍历 runtime_dir 各子目录读取 stats.json。
/// port_to_name 提供 sing-box 端口 → 实例名映射，用于把统一网关 node_stats.json
/// 中的 SOCKS5 出口地址（127.0.0.1:281xx）解析为实例名。
fn aggregate_stats(
    runtime_dir: &std::path::Path,
    known_names: &[String],
    port_to_name: &std::collections::HashMap<u16, String>,
) -> StatsSummary {
    let mut instances: Vec<InstanceStat> = Vec::new();
    let entries = match std::fs::read_dir(runtime_dir) {
        Ok(e) => e,
        Err(_) => {
            return StatsSummary {
                total_requests: 0,
                total_prompt_tokens: 0,
                total_completion_tokens: 0,
                total_tokens: 0,
                instances,
            };
        }
    };

    for entry in entries.flatten() {
        let dir_path = entry.path();
        if !dir_path.is_dir() {
            continue;
        }
        let name = entry.file_name().to_string_lossy().to_string();
        let stats_path = dir_path.join("stats.json");
        if !stats_path.exists() {
            continue;
        }
        let data = match std::fs::read_to_string(&stats_path) {
            Ok(d) => d,
            Err(_) => continue,
        };
        let go: GoStatsData = match serde_json::from_str(&data) {
            Ok(g) => g,
            Err(_) => continue,
        };

        let mut requests = 0i64;
        let mut prompt_tokens = 0i64;
        let mut completion_tokens = 0i64;
        let mut total_tokens = 0i64;
        let mut models: Vec<ModelStat> = Vec::new();

        for (model, ms) in go.models {
            requests += ms.request_count;
            prompt_tokens += ms.prompt_tokens;
            completion_tokens += ms.completion_tokens;
            total_tokens += ms.total_tokens;
            models.push(ModelStat {
                model,
                requests: ms.request_count,
                prompt_tokens: ms.prompt_tokens,
                completion_tokens: ms.completion_tokens,
                total_tokens: ms.total_tokens,
            });
        }
        // 模型明细按总计降序
        models.sort_by(|a, b| b.total_tokens.cmp(&a.total_tokens));

        let exists = known_names.contains(&name) || name == "_unified-gateway";
        let display_name = if name == "_unified-gateway" {
            "统一网关".to_string()
        } else {
            name.clone()
        };
        // 统一网关条目附带按节点拆分的统计（node_stats.json）
        let mut nodes: Vec<GatewayNodeStat> = Vec::new();
        if name == "_unified-gateway" {
            let node_path = dir_path.join("node_stats.json");
            if let Ok(data) = std::fs::read_to_string(&node_path) {
                if let Ok(gns) = serde_json::from_str::<GoNodeStatsData>(&data) {
                    for (addr, ns) in gns.nodes {
                        // addr 形如 127.0.0.1:28119，取端口反查实例名
                        let node_name = addr
                            .rsplit(':')
                            .next()
                            .and_then(|p| p.parse::<u16>().ok())
                            .and_then(|port| port_to_name.get(&port).cloned())
                            .unwrap_or_else(|| addr.clone());
                        nodes.push(GatewayNodeStat {
                            name: node_name,
                            addr,
                            requests: ns.request_count,
                            prompt_tokens: ns.prompt_tokens,
                            completion_tokens: ns.completion_tokens,
                            total_tokens: ns.total_tokens,
                        });
                    }
                    nodes.sort_by(|a, b| b.total_tokens.cmp(&a.total_tokens));
                }
            }
        }
        instances.push(InstanceStat {
            name: display_name,
            exists,
            requests,
            prompt_tokens,
            completion_tokens,
            total_tokens,
            models,
            nodes,
        });
    }

    // 实例按总计 token 降序
    instances.sort_by(|a, b| b.total_tokens.cmp(&a.total_tokens));

    StatsSummary {
        total_requests: instances.iter().map(|i| i.requests).sum(),
        total_prompt_tokens: instances.iter().map(|i| i.prompt_tokens).sum(),
        total_completion_tokens: instances.iter().map(|i| i.completion_tokens).sum(),
        total_tokens: instances.iter().map(|i| i.total_tokens).sum(),
        instances,
    }
}

fn csv_escape(s: &str) -> String {
    if s.contains(',') || s.contains('"') || s.contains('\n') || s.contains('\r') {
        format!("\"{}\"", s.replace('"', "\"\""))
    } else {
        s.to_string()
    }
}

/// 导出调用日志为 CSV 文本（调用日志页/统计页共用）。
/// 列按现有 CallLogRecord 字段：ts,model,status,path,err_msg,nodes,duration_ms,req_id。
pub fn export_call_log_csv_core(limit: Option<usize>) -> Result<String, String> {
    let (_, _, runtime_dir) = manager_paths();
    let path = runtime_dir.join("_unified-gateway").join("call_log.jsonl");
    let records = crate::call_log::read_call_log(&path, limit.unwrap_or(5000).clamp(1, 50000));
    let header = "ts,model,status,path,err_msg,nodes,duration_ms,req_id\n";
    let mut out = String::from(header);
    for r in records {
        out.push_str(&format!(
            "{},{},{},{},{},{},{},{}\n",
            csv_escape(&r.ts),
            csv_escape(&r.model),
            if r.has_issue() { "error" } else { "ok" },
            csv_escape(&r.path),
            csv_escape(&r.err_msg),
            csv_escape(&r.nodes.join("|")),
            r.duration_ms,
            csv_escape(&r.req_id),
        ));
    }
    Ok(out)
}

/// 导出全部实例快照为 JSON 文本
pub fn export_instances_json_core(core: &AppCore) -> Result<String, String> {
    let instances = list_instances_core(core)?;
    serde_json::to_string_pretty(&instances).map_err(|e| e.to_string())
}

/// 导出统计摘要为 JSON 文本
pub fn export_stats_json_core() -> Result<String, String> {
    let stats = get_stats_core()?;
    serde_json::to_string_pretty(&stats).map_err(|e| e.to_string())
}

#[tauri::command]
pub fn export_call_log_csv(limit: Option<usize>) -> Result<String, String> {
    export_call_log_csv_core(limit)
}

#[tauri::command]
pub fn export_instances_json(state: tauri::State<'_, AppState>) -> Result<String, String> {
    export_instances_json_core(&state.core)
}

#[tauri::command]
pub fn export_stats_json() -> Result<String, String> {
    export_stats_json_core()
}

#[cfg(test)]
mod stats_tests {
    use super::*;
    use std::fs;

    fn write_stats(dir: &std::path::Path, json: &str) {
        fs::create_dir_all(dir).unwrap();
        fs::write(dir.join("stats.json"), json).unwrap();
    }

    #[test]
    fn test_stats_aggregate_basic() {
        let root = std::env::temp_dir().join("opencode2api-stats-test-basic");
        let _ = fs::remove_dir_all(&root);
        write_stats(
            &root.join("user1"),
            r#"{"total_requests":2,"models":{"gpt-4o-mini":{"request_count":2,"prompt_tokens":400,"completion_tokens":70,"total_tokens":470}}}"#,
        );
        write_stats(
            &root.join("user2"),
            r#"{"total_requests":1,"models":{"claude-3-5":{"request_count":1,"prompt_tokens":100,"completion_tokens":30,"total_tokens":130}}}"#,
        );

        let known = vec!["user1".to_string(), "user2".to_string()];
        let s = aggregate_stats(&root, &known, &std::collections::HashMap::new());

        assert_eq!(s.total_requests, 3);
        assert_eq!(s.total_prompt_tokens, 500);
        assert_eq!(s.total_completion_tokens, 100);
        assert_eq!(s.total_tokens, 600);
        assert_eq!(s.instances.len(), 2);
        // 按总计降序：user1(470) 在前
        assert_eq!(s.instances[0].name, "user1");
        assert_eq!(s.instances[0].exists, true);
        assert_eq!(s.instances[0].models.len(), 1);
        assert_eq!(s.instances[0].models[0].model, "gpt-4o-mini");
        assert_eq!(s.instances[1].exists, true);
        let _ = fs::remove_dir_all(&root);
    }

    #[test]
    fn test_stats_deleted_instance_marked() {
        let root = std::env::temp_dir().join("opencode2api-stats-test-deleted");
        let _ = fs::remove_dir_all(&root);
        // user_old 目录存在但不在实例列表中（已删除）
        write_stats(
            &root.join("user_old"),
            r#"{"total_requests":5,"models":{"a":{"request_count":5,"prompt_tokens":50,"completion_tokens":5,"total_tokens":55}}}"#,
        );
        write_stats(
            &root.join("user_live"),
            r#"{"total_requests":1,"models":{"b":{"request_count":1,"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}}"#,
        );

        let known = vec!["user_live".to_string()];
        let s = aggregate_stats(&root, &known, &std::collections::HashMap::new());

        assert_eq!(s.instances.len(), 2);
        let old = s.instances.iter().find(|i| i.name == "user_old").unwrap();
        assert_eq!(old.exists, false);
        let live = s.instances.iter().find(|i| i.name == "user_live").unwrap();
        assert_eq!(live.exists, true);
        let _ = fs::remove_dir_all(&root);
    }

    #[test]
    fn test_stats_skips_missing_or_invalid() {
        let root = std::env::temp_dir().join("opencode2api-stats-test-skip");
        let _ = fs::remove_dir_all(&root);
        // 无 stats.json 的目录
        fs::create_dir_all(root.join("empty_dir")).unwrap();
        // stats.json 是坏 JSON
        write_stats(&root.join("bad_json"), r#"{not-json"#);
        // 正常实例
        write_stats(
            &root.join("good"),
            r#"{"total_requests":1,"models":{"m":{"request_count":1,"prompt_tokens":9,"completion_tokens":1,"total_tokens":10}}}"#,
        );

        let s = aggregate_stats(
            &root,
            &["good".to_string()],
            &std::collections::HashMap::new(),
        );
        assert_eq!(s.instances.len(), 1);
        assert_eq!(s.instances[0].name, "good");
        assert_eq!(s.total_requests, 1);
        let _ = fs::remove_dir_all(&root);
    }

    #[test]
    fn test_stats_models_sorted_desc() {
        let root = std::env::temp_dir().join("opencode2api-stats-test-sort");
        let _ = fs::remove_dir_all(&root);
        write_stats(
            &root.join("u"),
            r#"{"total_requests":2,"models":{"small":{"request_count":1,"prompt_tokens":1,"completion_tokens":1,"total_tokens":2},"big":{"request_count":1,"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}}"#,
        );

        let s = aggregate_stats(&root, &["u".to_string()], &std::collections::HashMap::new());
        assert_eq!(s.instances[0].models[0].model, "big");
        assert_eq!(s.instances[0].models[1].model, "small");
        let _ = fs::remove_dir_all(&root);
    }

    #[test]
    fn test_stats_gateway_nodes_resolved() {
        let root = std::env::temp_dir().join("opencode2api-stats-test-gw-nodes");
        let _ = fs::remove_dir_all(&root);
        write_stats(
            &root.join("_unified-gateway"),
            r#"{"total_requests":2,"models":{"deepseek":{"request_count":2,"prompt_tokens":300,"completion_tokens":200,"total_tokens":500}}}"#,
        );
        fs::write(
            root.join("_unified-gateway/node_stats.json"),
            r#"{"total_requests":2,"nodes":{"127.0.0.1:28100":{"request_count":1,"prompt_tokens":100,"completion_tokens":50,"total_tokens":150},"127.0.0.1:28112":{"request_count":1,"prompt_tokens":200,"completion_tokens":150,"total_tokens":350}}}"#,
        )
        .unwrap();

        let mut port_to_name = std::collections::HashMap::new();
        port_to_name.insert(28100u16, "荷兰①".to_string());
        port_to_name.insert(28112u16, "美国R1".to_string());
        let s = aggregate_stats(&root, &[], &port_to_name);

        assert_eq!(s.instances.len(), 1);
        let gw = &s.instances[0];
        assert_eq!(gw.name, "统一网关");
        assert_eq!(gw.exists, true);
        assert_eq!(gw.nodes.len(), 2);
        // 按总计降序：美国R1(350) 在前
        assert_eq!(gw.nodes[0].name, "美国R1");
        assert_eq!(gw.nodes[0].addr, "127.0.0.1:28112");
        assert_eq!(gw.nodes[0].total_tokens, 350);
        assert_eq!(gw.nodes[1].name, "荷兰①");
        assert_eq!(gw.nodes[1].total_tokens, 150);
        let _ = fs::remove_dir_all(&root);
    }

    #[test]
    fn test_stats_gateway_nodes_unmapped_addr() {
        let root = std::env::temp_dir().join("opencode2api-stats-test-gw-unmapped");
        let _ = fs::remove_dir_all(&root);
        write_stats(
            &root.join("_unified-gateway"),
            r#"{"total_requests":1,"models":{"m":{"request_count":1,"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}}"#,
        );
        // 端口不在映射表（实例已删除）→ 显示原始 addr
        fs::write(
            root.join("_unified-gateway/node_stats.json"),
            r#"{"total_requests":1,"nodes":{"127.0.0.1:28999":{"request_count":1,"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}}"#,
        )
        .unwrap();

        let s = aggregate_stats(&root, &[], &std::collections::HashMap::new());
        let gw = &s.instances[0];
        assert_eq!(gw.nodes.len(), 1);
        assert_eq!(gw.nodes[0].name, "127.0.0.1:28999");
        let _ = fs::remove_dir_all(&root);
    }
}

#[cfg(test)]
mod batch_add_tests {
    use super::*;
    use crate::instance::InstanceManager;
    use std::path::PathBuf;

    fn ephemeral_mgr() -> InstanceManager {
        // 不持久化：add_instance 不会写盘，测试无需清理
        InstanceManager::new_ephemeral(PathBuf::from("bin"), PathBuf::from("runtime"))
    }

    #[test]
    fn test_batch_add_dedup_same_node() {
        let mut mgr = ephemeral_mgr();
        // 先占用节点 A
        mgr.add_instance(
            "a-1".to_string(),
            18001,
            "节点A".to_string(),
            "sk-x".to_string(),
            "".to_string(),
        )
        .unwrap();

        let items = vec![
            BatchAddItem {
                node: "节点A".to_string(),
                name: None,
                port: None,
            },
            BatchAddItem {
                node: "节点B".to_string(),
                name: None,
                port: None,
            },
        ];
        let r = batch_add_inner(&mut mgr, &items, 30000, true, "n");
        assert_eq!(r.added_count, 1, "节点A 已存在应被去重，只应新增节点B");
        assert_eq!(r.error_count, 1);
        assert!(
            r.errors.iter().any(|e| e["node"] == "节点A"),
            "错误明细应包含已存在的节点A"
        );
        assert!(
            mgr.list_instances().iter().any(|i| i.node == "节点B"),
            "节点B 应被成功添加"
        );
    }

    #[test]
    fn test_batch_add_repeat_pool_is_idempotent() {
        let mut mgr = ephemeral_mgr();
        mgr.add_instance(
            "a-1".to_string(),
            18001,
            "节点A".to_string(),
            "sk-x".to_string(),
            "".to_string(),
        )
        .unwrap();
        mgr.set_join_gateway("a-1", true).unwrap();

        // 再次对同一节点入池：应被去重，不产生第二个实例
        let items = vec![BatchAddItem {
            node: "节点A".to_string(),
            name: None,
            port: None,
        }];
        let r = batch_add_inner(&mut mgr, &items, 30000, true, "n");
        assert_eq!(r.added_count, 0, "重复入池同一节点应被去重");
        assert_eq!(mgr.list_instances().len(), 1, "不应产生重复实例");
        assert!(
            mgr.list_instances()[0].join_gateway,
            "原实例的入池标记应保留"
        );
    }

    #[test]
    fn test_batch_add_name_conflict_gets_suffix() {
        let mut mgr = ephemeral_mgr();
        mgr.add_instance(
            "节点B".to_string(),
            18001,
            "节点B".to_string(),
            "sk-x".to_string(),
            "".to_string(),
        )
        .unwrap();

        // 不同节点、同名冲突：自动加后缀，仍应成功
        let items = vec![BatchAddItem {
            node: "节点C".to_string(),
            name: Some("节点B".to_string()),
            port: None,
        }];
        let r = batch_add_inner(&mut mgr, &items, 30000, true, "n");
        assert_eq!(r.added_count, 1);
        let names: Vec<&str> = mgr
            .list_instances()
            .iter()
            .map(|i| i.name.as_str())
            .collect();
        assert!(names.contains(&"节点B-2"), "同名冲突应加后缀: {:?}", names);
    }
}
