//! 节点探针：sing-box + opencode2api 实测节点，支持并发扫描。

use crate::clash_yaml::{self, ClashNode};
use crate::instance::{self, kill_process, no_window};
use crate::opencode_cfg;
use crate::singbox;
use anyhow::{bail, Context, Result};
use serde::{Deserialize, Serialize};
use std::fs;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

/// 探针默认 API 端口：debug 构建（tauri dev）用 39090 段，release 构建用生产 19090（与 main/web 探针段隔离）
#[cfg(debug_assertions)]
pub const DEFAULT_PROBE_API_PORT: u16 = 39090;
#[cfg(not(debug_assertions))]
pub const DEFAULT_PROBE_API_PORT: u16 = 19090;
/// 探针默认 sing-box SOCKS 端口：debug 构建用 49090 段，release 构建用生产 29090
#[cfg(debug_assertions)]
pub const DEFAULT_PROBE_SOCKS_PORT: u16 = 49090;
#[cfg(not(debug_assertions))]
pub const DEFAULT_PROBE_SOCKS_PORT: u16 = 29090;
/// 并发扫描最大 worker 数（默认上限；实际并发 = min(节点数, 请求并发, 可用端口对数)）
const MAX_SCAN_CONCURRENCY: usize = 8;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ScanStatus {
    Idle,
    Running,
    Stopping,
    Done,
    Error,
}

impl Default for ScanStatus {
    fn default() -> Self {
        ScanStatus::Idle
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProbeResult {
    pub node: String,
    pub node_type: String,
    pub server: String,
    pub port: u16,
    pub ok: bool,
    /// ok | config | socks | tls | upstream | timeout | other
    pub category: String,
    pub status_code: Option<u16>,
    pub model_count: Option<usize>,
    pub message: String,
    pub latency_ms: u64,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct ScanProgress {
    pub status: ScanStatus,
    pub total: usize,
    pub current: usize,
    pub current_node: Option<String>,
    pub results: Vec<ProbeResult>,
    pub error: Option<String>,
    pub api_port: u16,
    pub socks_port: u16,
    pub started_ms: Option<u64>,
    pub finished_ms: Option<u64>,
}

impl ScanProgress {
    pub fn snapshot(&self) -> Self {
        self.clone()
    }

    #[allow(dead_code)]
    pub fn ok_nodes(&self) -> Vec<&ProbeResult> {
        self.results.iter().filter(|r| r.ok).collect()
    }
}

/// 全局扫描控制器（Web / CLI 共用）
pub struct ScanController {
    pub progress: Arc<Mutex<ScanProgress>>,
    cancel: Arc<AtomicBool>,
    /// 防止并发启动两次扫描
    running: Arc<AtomicBool>,
}

impl ScanController {
    pub fn new() -> Self {
        Self {
            progress: Arc::new(Mutex::new(ScanProgress::default())),
            cancel: Arc::new(AtomicBool::new(false)),
            running: Arc::new(AtomicBool::new(false)),
        }
    }

    #[allow(dead_code)]
    pub fn is_running(&self) -> bool {
        self.running.load(Ordering::SeqCst)
    }

    pub fn progress_snapshot(&self) -> ScanProgress {
        self.progress
            .lock()
            .map(|g| g.snapshot())
            .unwrap_or_default()
    }

    pub fn request_stop(&self) {
        self.cancel.store(true, Ordering::SeqCst);
        if let Ok(mut g) = self.progress.lock() {
            if g.status == ScanStatus::Running {
                g.status = ScanStatus::Stopping;
            }
        }
    }

    /// 在后台线程启动扫描；若已在跑则报错。
    pub fn start_scan(
        &self,
        binary_dir: PathBuf,
        runtime_dir: PathBuf,
        password: String,
        api_port: u16,
        socks_port: u16,
        node_filter: Option<Vec<String>>,
        per_node_timeout_secs: u64,
        concurrency: Option<usize>,
    ) -> Result<()> {
        if self
            .running
            .compare_exchange(false, true, Ordering::SeqCst, Ordering::SeqCst)
            .is_err()
        {
            bail!("节点扫描已在进行中，请等待结束或先停止");
        }

        self.cancel.store(false, Ordering::SeqCst);

        let nodes = load_nodes_for_scan(node_filter)?;
        if nodes.is_empty() {
            self.running.store(false, Ordering::SeqCst);
            bail!("没有可扫描的节点（需含 password/uuid 等凭据）");
        }

        {
            let mut g = self
                .progress
                .lock()
                .map_err(|_| anyhow::anyhow!("扫描状态锁失败"))?;
            *g = ScanProgress {
                status: ScanStatus::Running,
                total: nodes.len(),
                current: 0,
                current_node: None,
                results: Vec::new(),
                error: None,
                api_port,
                socks_port,
                started_ms: Some(now_ms()),
                finished_ms: None,
            };
        }

        let progress = Arc::clone(&self.progress);
        let cancel = Arc::clone(&self.cancel);
        let running = Arc::clone(&self.running);

        thread::spawn(move || {
            let max_workers = concurrency
                .unwrap_or(MAX_SCAN_CONCURRENCY)
                .clamp(1, MAX_SCAN_CONCURRENCY);
            let result = run_scan_loop_parallel(
                &binary_dir,
                &runtime_dir,
                &password,
                api_port,
                socks_port,
                &nodes,
                max_workers,
                Duration::from_secs(per_node_timeout_secs.max(3)),
                &progress,
                &cancel,
            );

            if let Ok(mut g) = progress.lock() {
                g.finished_ms = Some(now_ms());
                g.current_node = None;
                match result {
                    Ok(()) => {
                        if cancel.load(Ordering::SeqCst) {
                            g.status = ScanStatus::Done;
                            g.error = Some("扫描已中止（已完成部分结果保留）".into());
                        } else {
                            g.status = ScanStatus::Done;
                        }
                    }
                    Err(e) => {
                        g.status = ScanStatus::Error;
                        g.error = Some(e.to_string());
                    }
                }
            }
            running.store(false, Ordering::SeqCst);
        });

        Ok(())
    }
}

impl Default for ScanController {
    fn default() -> Self {
        Self::new()
    }
}

fn now_ms() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}

fn load_nodes_for_scan(filter: Option<Vec<String>>) -> Result<Vec<ClashNode>> {
    let all = clash_yaml::list_nodes_with_group().context("读取代理节点失败")?;
    let with_cred: Vec<ClashNode> = all
        .into_iter()
        .filter(|n| n.password.is_some() || n.uuid.is_some())
        .collect();

    if let Some(names) = filter {
        if names.is_empty() {
            return Ok(with_cred);
        }
        let set: std::collections::HashSet<_> = names.into_iter().collect();
        Ok(with_cred
            .into_iter()
            .filter(|n| set.contains(&n.name))
            .collect())
    } else {
        Ok(with_cred)
    }
}

struct ProbeProcs {
    singbox: Option<Child>,
    singbox_port: Option<u16>,
    opencode: Option<Child>,
    opencode_port: Option<u16>,
}

impl Drop for ProbeProcs {
    fn drop(&mut self) {
        self.kill_all();
    }
}

impl ProbeProcs {
    fn kill_all(&mut self) {
        if let Some(mut c) = self.opencode.take() {
            let pid = c.id();
            let _ = c.kill();
            let _ = c.wait();
            let _ = kill_process(pid);
        }
        if let Some(mut c) = self.singbox.take() {
            let pid = c.id();
            let _ = c.kill();
            let _ = c.wait();
            let _ = kill_process(pid);
        }
        // 两个 worker 专用端口在下一个节点会复用：kill 后须等端口真正释放，
        // 否则下一节点 probe 的 ensure_port_available 失败 → "探测 API 进程启动失败"。
        // 本方法在每节点计时（ Instant::now()，见 probe_one_node_parallel）之外执行，
        // 等待不消耗 HTTP 探测预算，与 POST 探测自然解耦。
        if let Some(p) = self.singbox_port {
            wait_port_released(p, Duration::from_secs(3));
        }
        if let Some(p) = self.opencode_port {
            wait_port_released(p, Duration::from_secs(3));
        }
        self.singbox_port = None;
        self.opencode_port = None;
    }

    fn kill_singbox_only(&mut self) {
        if let Some(mut c) = self.singbox.take() {
            let pid = c.id();
            let _ = c.kill();
            let _ = c.wait();
            let _ = kill_process(pid);
        }
        let port = self.singbox_port.take();
        // 轮询等待旧进程释放所占端口，避免下一个节点立即复用被占用端口而误报启动失败。
        // 窗口要给足：过短会因端口未释放导致 ensure_port_available 失败（"探测 API 进程启动失败"）。
        // 此等待与 HTTP 探测预算解耦——预算由 probe 函数内独立的下限（见 http_timeout）兜底，
        // 二者不再互相压缩。
        if let Some(p) = port {
            wait_port_released(p, Duration::from_secs(3));
        }
    }

    fn kill_opencode_only(&mut self) {
        if let Some(mut c) = self.opencode.take() {
            let pid = c.id();
            let _ = c.kill();
            let _ = c.wait();
            let _ = kill_process(pid);
        }
        let port = self.opencode_port.take();
        // API 端口复用时同样给足释放窗口，避免下次探测 ensure_port_available 失败
        if let Some(p) = port {
            wait_port_released(p, Duration::from_secs(3));
        }
    }
}

/// 轮询等待本地端口真正释放（用于进程被杀后复用同一端口）。
/// 相比固定 sleep，能消除短时占用误报，且不影响真实占用时的最大等待。
fn wait_port_released(port: u16, timeout: Duration) {
    let deadline = Instant::now() + timeout;
    // 首检查一次即可快速返回（进程可能根本没在监听）
    loop {
        if instance::is_port_free(port) {
            return;
        }
        if Instant::now() >= deadline {
            return;
        }
        thread::sleep(Duration::from_millis(80));
    }
}

fn resolve_bin(dir: &Path, name: &str) -> Result<PathBuf> {
    // 平台化优先：Windows 先 .exe，其他平台先无扩展名（Linux 有残留 .exe 时
    // 也不会误选 Windows PE）。与 instance.rs resolve_platform_bin 保持一致。
    let bin = crate::instance::resolve_platform_bin(dir, name);
    if bin.exists() {
        return Ok(bin);
    }
    bail!(
        "未找到可执行文件: {} 或 {}",
        dir.join(format!("{}.exe", name)).display(),
        dir.join(name).display()
    );
}

// ======================== F7b 并发扫描 ========================

/// 为并发 worker 分配互不冲突的 (API, SOCKS) 端口对。
fn choose_probe_port_pairs(
    api_port: u16,
    socks_port: u16,
    count: usize,
) -> Result<Vec<(u16, u16)>> {
    let api_start = api_port.max(1024);
    let socks_start = socks_port.max(1024);
    let mut pairs = Vec::with_capacity(count);
    for offset in 0..512u16 {
        if pairs.len() == count {
            return Ok(pairs);
        }
        let Some(api) = api_start.checked_add(offset) else {
            break;
        };
        let Some(socks) = socks_start.checked_add(offset) else {
            break;
        };
        if api == socks
            || pairs
                .iter()
                .any(|(a, s)| *a == api || *s == api || *a == socks || *s == socks)
        {
            continue;
        }
        if instance::ensure_port_available(api).is_ok()
            && instance::ensure_port_available(socks).is_ok()
        {
            pairs.push((api, socks));
        }
    }
    bail!("无法为并发节点扫描找到 {} 组空闲 API/SOCKS 端口", count)
}

/// 在 worker 专属目录启动一个探测 opencode2api 进程（绑定独立端口）。
fn start_probe_opencode(
    procs: &mut ProbeProcs,
    oc_bin: &Path,
    oc_cfg_path: &Path,
    log_dir: &Path,
    api_port: u16,
    password: &str,
) -> Result<()> {
    procs.kill_opencode_only();
    instance::ensure_port_available(api_port)
        .with_context(|| format!("探测 API 端口 {} 仍被旧进程占用", api_port))?;
    let oc_out = fs::File::create(log_dir.join("opencode2api.out.log"))?;
    let oc_err = fs::File::create(log_dir.join("opencode2api.err.log"))?;
    let child = no_window(&mut Command::new(oc_bin))
        .arg("-port")
        .arg(api_port.to_string())
        .arg("-config")
        .arg(oc_cfg_path)
        .arg("-password")
        .arg(password)
        .stdout(Stdio::from(oc_out))
        .stderr(Stdio::from(oc_err))
        .spawn()
        .context("启动探测 opencode2api 失败")?;
    procs.opencode_port = Some(api_port);
    procs.opencode = Some(child);
    if !instance::wait_for_port(api_port, Duration::from_secs(15)) {
        procs.kill_opencode_only();
        bail!("探测 opencode2api 在 15s 内未监听 :{}", api_port);
    }
    Ok(())
}

/// 并发扫描：N 个 worker 各持独立端口对与进程，分摊节点列表。
fn run_scan_loop_parallel(
    binary_dir: &Path,
    runtime_dir: &Path,
    password: &str,
    api_port: u16,
    socks_port: u16,
    nodes: &[ClashNode],
    max_workers: usize,
    per_node_timeout: Duration,
    progress: &Arc<Mutex<ScanProgress>>,
    cancel: &Arc<AtomicBool>,
) -> Result<()> {
    let desired = nodes.len().min(max_workers).max(1);
    let port_pairs = choose_probe_port_pairs(api_port, socks_port, desired)?;
    // worker 数 = 实际分配到的端口对数（choose 失败时整体报错，不会静默降级）
    let worker_count = port_pairs.len().max(1);
    let probe_root = runtime_dir.join("_probe");
    fs::create_dir_all(&probe_root).context("创建并发探测目录失败")?;
    let singbox_bin = resolve_bin(binary_dir, "sing-box")?;
    let oc_bin = resolve_bin(binary_dir, "opencode2api")?;

    let mut worker_dirs = Vec::with_capacity(worker_count);
    for (worker_index, (_, socks_port)) in port_pairs.iter().enumerate() {
        let worker_dir = probe_root.join(format!("worker-{:02}", worker_index + 1));
        fs::create_dir_all(worker_dir.join("logs"))?;
        let oc_cfg = opencode_cfg::build_opencode_config(*socks_port)?;
        fs::write(worker_dir.join("opencode2api.json"), oc_cfg)?;
        worker_dirs.push(worker_dir);
    }

    thread::scope(|scope| -> Result<()> {
        let mut handles = Vec::with_capacity(worker_count);
        for worker_index in 0..worker_count {
            let worker_dir = worker_dirs[worker_index].clone();
            let (worker_api_port, worker_socks_port) = port_pairs[worker_index];
            let progress = Arc::clone(progress);
            let cancel = Arc::clone(cancel);
            let singbox_bin = singbox_bin.clone();
            let oc_bin = oc_bin.clone();
            handles.push(scope.spawn(move || -> Result<()> {
                let log_dir = worker_dir.join("logs");
                let oc_cfg_path = worker_dir.join("opencode2api.json");
                for (node_index, node) in nodes.iter().enumerate() {
                    if node_index % worker_count != worker_index {
                        continue;
                    }
                    if cancel.load(Ordering::SeqCst) {
                        break;
                    }

                    let mut procs = ProbeProcs {
                        singbox: None,
                        singbox_port: None,
                        opencode: None,
                        opencode_port: None,
                    };
                    let result = probe_one_node_parallel(
                        &mut procs,
                        node,
                        &singbox_bin,
                        &oc_bin,
                        &worker_dir,
                        &log_dir,
                        &oc_cfg_path,
                        worker_api_port,
                        worker_socks_port,
                        per_node_timeout,
                        password,
                    );
                    procs.kill_all();

                    if let Ok(mut state) = progress.lock() {
                        state.current += 1;
                        state.current_node = Some(node.name.clone());
                        state.results.push(result);
                    }
                }
                Ok(())
            }));
        }

        for handle in handles {
            handle
                .join()
                .map_err(|_| anyhow::anyhow!("并发节点探测线程异常退出"))??;
        }
        Ok(())
    })?;

    if let Ok(mut state) = progress.lock() {
        state.current_node = None;
    }
    Ok(())
}

#[allow(dead_code)] // 保留串行扫描实现（低并发回归用）
fn run_scan_loop(
    binary_dir: &Path,
    runtime_dir: &Path,
    password: &str,
    api_port: u16,
    socks_port: u16,
    nodes: &[ClashNode],
    per_node_timeout: Duration,
    progress: &Arc<Mutex<ScanProgress>>,
    cancel: &Arc<AtomicBool>,
) -> Result<()> {
    let probe_dir = runtime_dir.join("_probe");
    let log_dir = probe_dir.join("logs");
    fs::create_dir_all(&log_dir).context("创建探针目录失败")?;

    let singbox_bin = resolve_bin(binary_dir, "sing-box")?;
    let oc_bin = resolve_bin(binary_dir, "opencode2api")?;

    // 固定 opencode 配置
    let oc_cfg = opencode_cfg::build_opencode_config(socks_port)?;
    let oc_cfg_path = probe_dir.join("opencode2api.json");
    fs::write(&oc_cfg_path, oc_cfg).context("写入探针 opencode 配置失败")?;

    let mut procs = ProbeProcs {
        singbox: None,
        singbox_port: None,
        opencode: None,
        opencode_port: None,
    };

    for (i, node) in nodes.iter().enumerate() {
        if cancel.load(Ordering::SeqCst) {
            break;
        }

        if let Ok(mut g) = progress.lock() {
            g.current = i + 1;
            g.current_node = Some(node.name.clone());
        }

        let result = probe_one_node(
            &mut procs,
            node,
            &singbox_bin,
            &oc_bin,
            &oc_cfg_path,
            &probe_dir,
            &log_dir,
            api_port,
            socks_port,
            password,
            per_node_timeout,
        );

        if let Ok(mut g) = progress.lock() {
            g.results.push(result);
        }
    }

    procs.kill_all();
    Ok(())
}

fn probe_one_node(
    procs: &mut ProbeProcs,
    node: &ClashNode,
    singbox_bin: &Path,
    opencode_bin: &Path,
    opencode_cfg: &Path,
    probe_dir: &Path,
    log_dir: &Path,
    api_port: u16,
    socks_port: u16,
    password: &str,
    per_node_timeout: Duration,
) -> ProbeResult {
    let start = Instant::now();
    let base =
        |ok: bool, category: &str, message: String, status: Option<u16>, models: Option<usize>| {
            ProbeResult {
                node: node.name.clone(),
                node_type: node.node_type.clone(),
                server: node.server.clone(),
                port: node.port,
                ok,
                category: category.to_string(),
                status_code: status,
                model_count: models,
                message,
                latency_ms: start.elapsed().as_millis() as u64,
            }
        };

    // 生成 sing-box 配置
    let cfg = match singbox::build_singbox_config(node, socks_port) {
        Ok(c) => c,
        Err(e) => {
            return base(false, "config", format!("生成配置失败: {}", e), None, None);
        }
    };
    let cfg_path = probe_dir.join("singbox.json");
    if let Err(e) = fs::write(&cfg_path, &cfg) {
        return base(false, "config", format!("写入配置失败: {}", e), None, None);
    }

    // 重启 sing-box
    procs.kill_singbox_only();

    let sb_out = match fs::File::create(log_dir.join("singbox.out.log")) {
        Ok(f) => f,
        Err(e) => return base(false, "other", format!("日志文件失败: {}", e), None, None),
    };
    let sb_err = match fs::File::create(log_dir.join("singbox.err.log")) {
        Ok(f) => f,
        Err(e) => return base(false, "other", format!("日志文件失败: {}", e), None, None),
    };

    let child = match no_window(&mut Command::new(singbox_bin))
        .args(["run", "-c"])
        .arg(&cfg_path)
        .stdout(Stdio::from(sb_out))
        .stderr(Stdio::from(sb_err))
        .spawn()
    {
        Ok(c) => c,
        Err(e) => {
            return base(
                false,
                "other",
                format!("启动 sing-box 失败: {}", e),
                None,
                None,
            );
        }
    };
    procs.singbox_port = Some(socks_port);
    procs.singbox = Some(child);

    let socks_wait = Duration::from_secs(8).min(per_node_timeout);
    if !instance::wait_for_port(socks_port, socks_wait) {
        // 读一下 err 日志给提示
        let hint = tail_of_file(&log_dir.join("singbox.err.log"), 300);
        let cat = if hint.to_lowercase().contains("tls")
            || hint.contains("certificate")
            || hint.contains("handshake")
        {
            "tls"
        } else {
            "socks"
        };
        return base(
            false,
            cat,
            format!(
                "sing-box SOCKS :{} 未就绪。{}",
                socks_port,
                if hint.is_empty() {
                    String::new()
                } else {
                    format!("日志: {}", truncate_str(&hint, 180))
                }
            ),
            None,
            None,
        );
    }

    // 稍等出口握手
    thread::sleep(Duration::from_millis(400));

    // 每个节点都重启 opencode2api：Go 端 modelsCache 一旦被某个可用节点填满就会永久命中，
    // 后续节点即使代理完全不通也会拿到 HTTP 200（假阳性）。重启让启动期的
    // fetchModels() 成为对当前节点的真实探测。
    procs.kill_opencode_only();

    let oc_out = match fs::File::create(log_dir.join("opencode2api.out.log")) {
        Ok(f) => f,
        Err(e) => return base(false, "other", format!("日志文件失败: {}", e), None, None),
    };
    let oc_err = match fs::File::create(log_dir.join("opencode2api.err.log")) {
        Ok(f) => f,
        Err(e) => return base(false, "other", format!("日志文件失败: {}", e), None, None),
    };
    let oc_child = match no_window(&mut Command::new(opencode_bin))
        .arg("-port")
        .arg(api_port.to_string())
        .arg("-config")
        .arg(opencode_cfg)
        .arg("-password")
        .arg(password)
        .stdout(Stdio::from(oc_out))
        .stderr(Stdio::from(oc_err))
        .spawn()
    {
        Ok(c) => c,
        Err(e) => {
            return base(
                false,
                "other",
                format!("启动 opencode2api 失败: {}", e),
                None,
                None,
            );
        }
    };
    procs.opencode = Some(oc_child);

    // opencode2api 会在启动时同步拉取模型列表，之后才 listen；
    // 端口迟迟不监听基本等于当前节点无法访问上游。
    let remain = per_node_timeout.saturating_sub(start.elapsed());
    let api_wait = remain
        .max(Duration::from_secs(5))
        .min(Duration::from_secs(20));
    if !instance::wait_for_port(api_port, api_wait) {
        let hint = tail_of_file(&log_dir.join("opencode2api.out.log"), 300);
        return base(
            false,
            "upstream",
            format!(
                "opencode2api :{} 未在 {}s 内就绪（上游不可达）。{}",
                api_port,
                api_wait.as_secs(),
                if hint.is_empty() {
                    String::new()
                } else {
                    format!("日志: {}", truncate_str(&hint, 180))
                }
            ),
            None,
            None,
        );
    }

    let remain = per_node_timeout.saturating_sub(start.elapsed());
    let http_timeout = remain
        .max(Duration::from_secs(4))
        .min(Duration::from_secs(12));

    match instance::probe_free_completion_response(api_port, Some(password), http_timeout) {
        Ok((status, body)) => {
            if instance::is_probe_completion_success(status, &body) {
                let model_count = count_models(&body);
                base(
                    true,
                    "ok",
                    match model_count {
                        Some(n) => format!("可用，models={}", n),
                        None => "可用（免费模型最小请求成功）".into(),
                    },
                    Some(status),
                    model_count,
                )
            } else {
                let cat = if (200..300).contains(&status) {
                    "invalid_response"
                } else if status == 503 || status == 502 || status == 504 {
                    "upstream"
                } else {
                    "other"
                };
                base(
                    false,
                    cat,
                    format!("HTTP {}，{}", status, truncate_str(&body, 160)),
                    Some(status),
                    None,
                )
            }
        }
        Err(e) => {
            let msg = e.to_string();
            let cat = if msg.contains("timed out") || msg.contains("超时") {
                "timeout"
            } else {
                "other"
            };
            base(false, cat, format!("请求失败: {}", msg), None, None)
        }
    }
}

/// 并行版单节点探测：worker 专属目录 + 独立 opencode 进程（F7b）。
fn probe_one_node_parallel(
    procs: &mut ProbeProcs,
    node: &ClashNode,
    singbox_bin: &Path,
    oc_bin: &Path,
    probe_dir: &Path,
    log_dir: &Path,
    oc_cfg_path: &Path,
    api_port: u16,
    socks_port: u16,
    per_node_timeout: Duration,
    password: &str,
) -> ProbeResult {
    let start = Instant::now();
    let base =
        |ok: bool, category: &str, message: String, status: Option<u16>, models: Option<usize>| {
            ProbeResult {
                node: node.name.clone(),
                node_type: node.node_type.clone(),
                server: node.server.clone(),
                port: node.port,
                ok,
                category: category.to_string(),
                status_code: status,
                model_count: models,
                message,
                latency_ms: start.elapsed().as_millis() as u64,
            }
        };

    // 生成 sing-box 配置
    let cfg = match singbox::build_singbox_config(node, socks_port) {
        Ok(c) => c,
        Err(e) => {
            return base(false, "config", format!("生成配置失败: {}", e), None, None);
        }
    };
    let cfg_path = probe_dir.join("singbox.json");
    if let Err(e) = fs::write(&cfg_path, &cfg) {
        return base(false, "config", format!("写入配置失败: {}", e), None, None);
    }

    // 重启 sing-box
    procs.kill_singbox_only();
    if let Err(e) = instance::ensure_port_available(socks_port) {
        return base(
            false,
            "local",
            format!("SOCKS 端口 {} 仍被旧进程占用: {}", socks_port, e),
            None,
            None,
        );
    }

    let sb_out = match fs::File::create(log_dir.join("singbox.out.log")) {
        Ok(f) => f,
        Err(e) => return base(false, "other", format!("日志文件失败: {}", e), None, None),
    };
    let sb_err = match fs::File::create(log_dir.join("singbox.err.log")) {
        Ok(f) => f,
        Err(e) => return base(false, "other", format!("日志文件失败: {}", e), None, None),
    };

    let child = match no_window(&mut Command::new(singbox_bin))
        .args(["run", "-c"])
        .arg(&cfg_path)
        .stdout(Stdio::from(sb_out))
        .stderr(Stdio::from(sb_err))
        .spawn()
    {
        Ok(c) => c,
        Err(e) => {
            return base(
                false,
                "other",
                format!("启动 sing-box 失败: {}", e),
                None,
                None,
            );
        }
    };
    procs.singbox_port = Some(socks_port);
    procs.singbox = Some(child);

    let socks_wait = Duration::from_secs(8).min(per_node_timeout);
    if !instance::wait_for_port(socks_port, socks_wait) {
        // 读一下 err 日志给提示
        let hint = fs::read_to_string(log_dir.join("singbox.err.log")).unwrap_or_default();
        let hint = hint
            .chars()
            .rev()
            .take(300)
            .collect::<String>()
            .chars()
            .rev()
            .collect::<String>();
        let cat = if hint.to_lowercase().contains("tls")
            || hint.contains("certificate")
            || hint.contains("handshake")
        {
            "tls"
        } else {
            "socks"
        };
        return base(
            false,
            cat,
            format!(
                "sing-box SOCKS :{} 未就绪。{}",
                socks_port,
                if hint.is_empty() {
                    String::new()
                } else {
                    format!("日志: {}", truncate_str(&hint, 180))
                }
            ),
            None,
            None,
        );
    }

    // 稍等出口握手
    thread::sleep(Duration::from_millis(400));

    // 并行 worker 各自启动独立 opencode（绑定本 worker 专属端口）
    if let Err(e) = start_probe_opencode(procs, oc_bin, oc_cfg_path, log_dir, api_port, password) {
        return base(
            false,
            "local",
            format!("探测 API 进程启动失败: {}", e),
            None,
            None,
        );
    }

    let remain = per_node_timeout.saturating_sub(start.elapsed());
    let http_timeout = remain
        .max(Duration::from_secs(4))
        .min(Duration::from_secs(12));

    // F2: 免费额度实测——先取模型目录，再发 1 token 最小请求，能出 choices 才算可用
    match instance::probe_free_completion_response(api_port, Some(password), http_timeout) {
        Ok((status, body)) => {
            if instance::is_probe_completion_success(status, &body) {
                let model_count = count_models(&body);
                base(
                    true,
                    "ok",
                    match model_count {
                        Some(n) => format!("可用，models={}", n),
                        None => "可用（免费模型最小请求成功）".into(),
                    },
                    Some(status),
                    model_count,
                )
            } else {
                let cat = if (200..300).contains(&status) {
                    "invalid_response"
                } else if status == 503 || status == 502 || status == 504 {
                    "upstream"
                } else {
                    "other"
                };
                base(
                    false,
                    cat,
                    format!("HTTP {}，{}", status, truncate_str(&body, 160)),
                    Some(status),
                    None,
                )
            }
        }
        Err(e) => {
            let msg = e.to_string();
            let cat = if msg.contains("timed out") || msg.contains("超时") {
                "timeout"
            } else {
                "other"
            };
            base(false, cat, format!("请求失败: {}", msg), None, None)
        }
    }
}

fn count_models(body: &str) -> Option<usize> {
    let v: serde_json::Value = serde_json::from_str(body).ok()?;
    if let Some(arr) = v.get("data").and_then(|d| d.as_array()) {
        return Some(arr.len());
    }
    v.as_array().map(|a| a.len())
}

fn truncate_str(s: &str, max: usize) -> String {
    let mut t: String = s.chars().take(max).collect();
    if s.chars().count() > max {
        t.push('…');
    }
    t
}

/// 读取文件尾部最多 max_chars 个字符，用于错误提示。
fn tail_of_file(path: &Path, max_chars: usize) -> String {
    let s = fs::read_to_string(path).unwrap_or_default();
    let mut chars: Vec<char> = s.chars().collect();
    if chars.len() > max_chars {
        chars = chars.split_off(chars.len() - max_chars);
    }
    chars.into_iter().collect::<String>().trim().to_string()
}

/// 同步执行完整扫描（CLI 用），返回结果列表。
pub fn scan_nodes_sync(
    binary_dir: PathBuf,
    runtime_dir: PathBuf,
    password: String,
    api_port: u16,
    socks_port: u16,
    node_filter: Option<Vec<String>>,
    per_node_timeout_secs: u64,
    on_progress: impl Fn(&ScanProgress),
) -> Result<Vec<ProbeResult>> {
    let nodes = load_nodes_for_scan(node_filter)?;
    if nodes.is_empty() {
        bail!("没有可扫描的节点");
    }
    let progress = Arc::new(Mutex::new(ScanProgress {
        status: ScanStatus::Running,
        total: nodes.len(),
        current: 0,
        current_node: None,
        results: Vec::new(),
        error: None,
        api_port,
        socks_port,
        started_ms: Some(now_ms()),
        finished_ms: None,
    }));
    let cancel = Arc::new(AtomicBool::new(false));

    // 包装 progress 回调：每节点后通知
    let progress_cb = Arc::clone(&progress);
    // 简单轮询式：在 loop 内手动 — 这里直接 run 后读

    // 自定义 loop 带回调
    let probe_dir = runtime_dir.join("_probe");
    let log_dir = probe_dir.join("logs");
    fs::create_dir_all(&log_dir)?;

    let singbox_bin = resolve_bin(&binary_dir, "sing-box")?;
    let oc_bin = resolve_bin(&binary_dir, "opencode2api")?;

    let oc_cfg = opencode_cfg::build_opencode_config(socks_port)?;
    let oc_cfg_path = probe_dir.join("opencode2api.json");
    fs::write(&oc_cfg_path, oc_cfg)?;

    let mut procs = ProbeProcs {
        singbox: None,
        singbox_port: None,
        opencode: None,
        opencode_port: None,
    };

    let timeout = Duration::from_secs(per_node_timeout_secs.max(3));
    for (i, node) in nodes.iter().enumerate() {
        if cancel.load(Ordering::SeqCst) {
            break;
        }
        {
            let mut g = progress_cb
                .lock()
                .map_err(|_| anyhow::anyhow!("扫描状态锁失败"))?;
            g.current = i + 1;
            g.current_node = Some(node.name.clone());
            on_progress(&g.snapshot());
        }

        let result = probe_one_node(
            &mut procs,
            node,
            &singbox_bin,
            &oc_bin,
            &oc_cfg_path,
            &probe_dir,
            &log_dir,
            api_port,
            socks_port,
            &password,
            timeout,
        );

        {
            let mut g = progress_cb
                .lock()
                .map_err(|_| anyhow::anyhow!("扫描状态锁失败"))?;
            g.results.push(result);
            on_progress(&g.snapshot());
        }
    }

    procs.kill_all();

    let mut g = progress
        .lock()
        .map_err(|_| anyhow::anyhow!("扫描状态锁失败"))?;
    g.status = ScanStatus::Done;
    g.finished_ms = Some(now_ms());
    g.current_node = None;
    Ok(g.results.clone())
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 探针端口三环境隔离：debug 构建（tauri dev）用 39090/49090 段，release 构建用生产 19090/29090
    #[test]
    fn probe_ports_isolated_by_build() {
        #[cfg(debug_assertions)]
        {
            assert_eq!(DEFAULT_PROBE_API_PORT, 39090);
            assert_eq!(DEFAULT_PROBE_SOCKS_PORT, 49090);
        }
        #[cfg(not(debug_assertions))]
        {
            assert_eq!(DEFAULT_PROBE_API_PORT, 19090);
            assert_eq!(DEFAULT_PROBE_SOCKS_PORT, 29090);
        }
    }

    #[cfg(windows)]
    #[test]
    fn test_kill_opencode_only_preserves_singbox() {
        let mut procs = ProbeProcs {
            singbox: Some(spawn_sleeper()),
            singbox_port: None,
            opencode: Some(spawn_sleeper()),
            opencode_port: None,
        };
        procs.kill_opencode_only();
        assert!(procs.opencode.is_none());
        assert!(procs.singbox.is_some());
        let mut child = procs.singbox.take().unwrap();
        let _ = child.kill();
        let _ = child.wait();
    }

    #[cfg(windows)]
    fn spawn_sleeper() -> Child {
        Command::new("powershell.exe")
            .args(["-NoProfile", "-Command", "Start-Sleep -Seconds 30"])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("spawn sleeper")
    }

    #[test]
    fn wait_port_released_returns_when_free() {
        // 绑定一个端口，另起线程在 300ms 后释放，验证轮询在超时前返回
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        let handle = std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(300));
            drop(listener);
        });
        let started = Instant::now();
        wait_port_released(port, Duration::from_secs(3));
        let elapsed = started.elapsed();
        handle.join().unwrap();
        assert!(
            elapsed < Duration::from_secs(3),
            "应在端口释放后立即返回，实际等了 {:?}",
            elapsed
        );
    }

    #[test]
    fn wait_port_released_returns_fast_when_free() {
        // 端口本就空闲时首次检查即返回，不应有任何轮询延迟
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        drop(listener);
        let started = Instant::now();
        wait_port_released(port, Duration::from_secs(3));
        assert!(started.elapsed() < Duration::from_millis(200));
    }
}
