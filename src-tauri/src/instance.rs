use crate::clash_yaml;
use crate::opencode_cfg;
use crate::singbox;
use anyhow::{bail, Context, Result};
use serde::{Deserialize, Serialize};
use std::fs;
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::thread;
use std::time::{Duration, Instant};
/// 设置子进程在 Windows 下**不创建控制台窗口**（CREATE_NO_WINDOW = 0x08000000）。
/// 否则每次 spawn/调用子进程（sing-box、opencode2api、taskkill 等）都会弹出一个 cmd 窗口。
/// 非 Windows 平台原样返回。
pub(crate) fn no_window(cmd: &mut Command) -> &mut Command {
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        cmd.creation_flags(0x08000000);
    }
    cmd
}

/// 解析子程序可执行文件路径：Windows 优先 `<name>.exe`，其他平台优先无扩展名
/// `<name>`（与 embed.rs 的 platform_name 释放逻辑对应，避免 Linux 误选残留的
/// Windows PE .exe 而无法执行）。
pub(crate) fn resolve_platform_bin(bin_dir: &Path, name: &str) -> PathBuf {
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

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct Instance {
    pub name: String,
    pub port: u16,
    pub node: String,
    #[serde(default)]
    pub password: String,
    #[serde(default)]
    pub ip: String,
    pub singbox_port: u16,
    pub pid: Option<u32>,
    pub singbox_pid: Option<u32>,
    #[serde(default)]
    pub join_gateway: bool,
    pub status: InstanceStatus,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, Default)]
pub enum InstanceStatus {
    #[default]
    Stopped,
    Starting,
    Running,
    Stopping,
    Error(String),
}

pub struct InstanceManager {
    pub instances: Vec<Instance>,
    pub config_path: PathBuf,
    pub binary_dir: PathBuf,
    pub runtime_dir: PathBuf,
    persist_state: bool,
}

impl InstanceManager {
    pub fn new(config_path: PathBuf, binary_dir: PathBuf, runtime_dir: PathBuf) -> Self {
        InstanceManager {
            instances: Vec::new(),
            config_path,
            binary_dir,
            runtime_dir,
            persist_state: true,
        }
    }

    /// 临时 manager：不持久化共享状态，供并行启动任务的 worker 安全使用。
    pub fn new_ephemeral(binary_dir: PathBuf, runtime_dir: PathBuf) -> Self {
        Self {
            instances: Vec::new(),
            config_path: PathBuf::new(),
            binary_dir,
            runtime_dir,
            persist_state: false,
        }
    }

    pub fn add_instance(
        &mut self,
        name: String,
        port: u16,
        node: String,
        password: String,
        ip: String,
    ) -> Result<()> {
        if self.instances.iter().any(|i| i.name == name) {
            bail!("实例 '{}' 已存在", name);
        }
        if self.instances.iter().any(|i| i.port == port) {
            bail!("端口 {} 已被其他实例占用", port);
        }
        let instance = Instance {
            name,
            port,
            node,
            password,
            ip,
            singbox_port: port + 10000,
            pid: None,
            singbox_pid: None,
            join_gateway: false,
            status: InstanceStatus::Stopped,
        };
        self.instances.push(instance);
        self.save()?;
        Ok(())
    }

    /// 移除实例（释放回节点扫描）：若正在运行/启动/停止中，先自动关闭（kill 进程），再删除记录。
    pub fn remove_instance(&mut self, name: &str) -> Result<()> {
        let idx = self
            .instances
            .iter()
            .position(|i| i.name == name)
            .context("实例不存在")?;
        // 一条龙：无论当前状态如何，先关闭进程再删除（无需手动先停止）
        if let Some(pid) = self.instances[idx].pid {
            let _ = kill_process(pid);
        }
        if let Some(pid) = self.instances[idx].singbox_pid {
            let _ = kill_process(pid);
        }
        self.instances.remove(idx);
        self.save()?;
        Ok(())
    }

    pub fn start_instance_inner(&mut self, name: &str) -> Result<()> {
        let idx = self
            .instances
            .iter()
            .position(|i| i.name == name)
            .context("实例不存在")?;

        if self.instances[idx].status == InstanceStatus::Running {
            bail!("实例 '{}' 已在运行", name);
        }

        let mut password = self.instances[idx].password.clone();
        if password.is_empty() {
            password = crate::config::Config::effective_default_password();
        }

        // 1. 根据节点名查找 Clash 节点
        let nodes =
            clash_yaml::list_nodes_with_group().context("无法读取代理节点（本地或外部控制）")?;
        let node = nodes
            .iter()
            .find(|n| n.name == self.instances[idx].node)
            .with_context(|| format!("未找到节点 '{}'", self.instances[idx].node))?;

        let instance_dir = self.runtime_dir.join(&self.instances[idx].name);
        fs::create_dir_all(&instance_dir).context("创建实例目录失败")?;
        let log_dir = instance_dir.join("logs");
        fs::create_dir_all(&log_dir).context("创建日志目录失败")?;

        self.instances[idx].status = InstanceStatus::Starting;
        self.save()?;

        // 2. 生成并启动 sing-box
        let singbox_cfg = singbox::build_singbox_config(node, self.instances[idx].singbox_port)
            .context("生成 sing-box 配置失败")?;
        let singbox_cfg_path = instance_dir.join("singbox.json");
        fs::write(&singbox_cfg_path, singbox_cfg).context("写入 sing-box 配置失败")?;

        let singbox_bin = resolve_platform_bin(&self.binary_dir, "sing-box");
        if !singbox_bin.exists() {
            bail!(
                "未找到 sing-box 可执行文件: {}",
                self.binary_dir.join("sing-box.exe").display()
            );
        }

        let singbox_stdout = fs::File::create(log_dir.join("singbox.out.log"))
            .context("创建 sing-box 输出日志失败")?;
        let singbox_stderr = fs::File::create(log_dir.join("singbox.err.log"))
            .context("创建 sing-box 错误日志失败")?;

        let singbox_child = no_window(&mut Command::new(&singbox_bin))
            .args(["run", "-c"])
            .arg(&singbox_cfg_path)
            .stdout(Stdio::from(singbox_stdout))
            .stderr(Stdio::from(singbox_stderr))
            .spawn()
            .context("启动 sing-box 失败")?;
        self.instances[idx].singbox_pid = Some(singbox_child.id());

        // sing-box 已启动；后续任何失败都必须先清理它，避免孤儿进程占用端口。
        // 用闭包统一收尾：失败时杀 sing-box 并回写 Error 状态。
        let cleanup_singbox = |instances: &mut [Instance], idx: usize| {
            if let Some(pid) = instances[idx].singbox_pid.take() {
                let _ = kill_process(pid);
            }
            instances[idx].status = InstanceStatus::Error("opencode2api 启动失败".into());
            instances[idx].pid = None;
        };

        // 等待 sing-box SOCKS5 端口就绪，再启动 opencode2api
        let singbox_port = self.instances[idx].singbox_port;
        if !wait_for_port(singbox_port, Duration::from_secs(10)) {
            let _ = kill_process(singbox_child.id());
            self.instances[idx].status = InstanceStatus::Error("sing-box 端口未就绪".into());
            self.instances[idx].singbox_pid = None;
            self.save()?;
            bail!("sing-box 在 10s 内未能监听 127.0.0.1:{}", singbox_port);
        }

        // 3. 生成并启动 opencode2api
        let oc_cfg = opencode_cfg::build_opencode_config(self.instances[idx].singbox_port)
            .map_err(|e| {
                cleanup_singbox(&mut self.instances, idx);
                e
            })?;
        let oc_cfg_path = instance_dir.join("opencode2api.json");
        fs::write(&oc_cfg_path, oc_cfg).map_err(|e| {
            cleanup_singbox(&mut self.instances, idx);
            e
        })?;

        let oc_bin = resolve_platform_bin(&self.binary_dir, "opencode2api");
        if !oc_bin.exists() {
            cleanup_singbox(&mut self.instances, idx);
            bail!(
                "未找到 opencode2api 可执行文件: {}",
                self.binary_dir.join("opencode2api.exe").display()
            );
        }

        let oc_stdout = fs::File::create(log_dir.join("opencode2api.out.log")).map_err(|e| {
            cleanup_singbox(&mut self.instances, idx);
            e
        })?;
        let oc_stderr = fs::File::create(log_dir.join("opencode2api.err.log")).map_err(|e| {
            cleanup_singbox(&mut self.instances, idx);
            e
        })?;

        let oc_child = no_window(&mut Command::new(&oc_bin))
            // 工作目录设为实例专属目录：Go 核心把 stats.json 写入当前工作目录，
            // 隔离后每个实例的 token 统计独立落盘到 runtime/{实例名}/stats.json
            .current_dir(&instance_dir)
            .arg("-port")
            .arg(self.instances[idx].port.to_string())
            .arg("-config")
            .arg(&oc_cfg_path)
            .arg("-password")
            .arg(password)
            .stdout(Stdio::from(oc_stdout))
            .stderr(Stdio::from(oc_stderr))
            .spawn()
            .map_err(|e| {
                cleanup_singbox(&mut self.instances, idx);
                e
            })?;
        self.instances[idx].pid = Some(oc_child.id());

        let api_port = self.instances[idx].port;
        if !wait_for_port(api_port, Duration::from_secs(15)) {
            let _ = kill_process(oc_child.id());
            let _ = kill_process(singbox_child.id());
            self.instances[idx].status = InstanceStatus::Error("opencode2api 端口未就绪".into());
            self.instances[idx].pid = None;
            self.instances[idx].singbox_pid = None;
            self.save()?;
            bail!("opencode2api 在 15s 内未能监听 0.0.0.0:{}", api_port);
        }

        self.instances[idx].status = InstanceStatus::Running;
        self.save()?;
        Ok(())
    }

    /// 短锁标记实例为 Starting 并返回快照（放锁后供并行 worker 启动）。
    pub fn mark_starting(&mut self, name: &str) -> Result<Instance> {
        let idx = self
            .instances
            .iter()
            .position(|instance| instance.name == name)
            .context("实例不存在")?;
        if matches!(
            self.instances[idx].status,
            InstanceStatus::Running | InstanceStatus::Starting | InstanceStatus::Stopping
        ) {
            bail!("实例 '{}' 正在忙", name);
        }
        let mut instance = self.instances[idx].clone();
        instance.status = InstanceStatus::Starting;
        instance.pid = None;
        instance.singbox_pid = None;
        self.instances[idx] = instance.clone();
        Ok(instance)
    }

    /// 短锁回写并行启动结果。
    pub fn apply_start_result(
        &mut self,
        name: &str,
        result: std::result::Result<Instance, String>,
    ) -> Result<()> {
        let idx = self
            .instances
            .iter()
            .position(|instance| instance.name == name)
            .context("实例不存在")?;
        match result {
            Ok(mut instance) => {
                instance.status = InstanceStatus::Running;
                self.instances[idx] = instance;
                Ok(())
            }
            Err(error) => {
                self.instances[idx].status = InstanceStatus::Error(error.clone());
                self.instances[idx].pid = None;
                self.instances[idx].singbox_pid = None;
                bail!("{}", error)
            }
        }
    }

    /// 短锁标记实例为 Stopping 并取出 PID（放锁后供并行 worker 杀进程）。
    pub fn prepare_stop(&mut self, name: &str) -> Result<(Option<u32>, Option<u32>)> {
        let idx = self
            .instances
            .iter()
            .position(|instance| instance.name == name)
            .context("实例不存在")?;
        if matches!(
            self.instances[idx].status,
            InstanceStatus::Starting | InstanceStatus::Stopping
        ) {
            bail!("实例 '{}' 正在忙", name);
        }
        let pids = (self.instances[idx].pid, self.instances[idx].singbox_pid);
        self.instances[idx].status = InstanceStatus::Stopping;
        Ok(pids)
    }

    /// 短锁回写停止完成状态。
    pub fn finish_stop(&mut self, name: &str) -> Result<()> {
        let instance = self
            .instances
            .iter_mut()
            .find(|instance| instance.name == name)
            .context("实例不存在")?;
        instance.status = InstanceStatus::Stopped;
        instance.pid = None;
        instance.singbox_pid = None;
        Ok(())
    }

    pub fn stop_instance(&mut self, name: &str) -> Result<()> {
        let idx = self
            .instances
            .iter()
            .position(|i| i.name == name)
            .context("实例不存在")?;

        self.instances[idx].status = InstanceStatus::Stopping;
        self.save()?;

        // 先停 opencode2api，再停 sing-box
        if let Some(pid) = self.instances[idx].pid {
            kill_process(pid).ok();
        }
        if let Some(pid) = self.instances[idx].singbox_pid {
            kill_process(pid).ok();
        }

        self.instances[idx].status = InstanceStatus::Stopped;
        self.instances[idx].pid = None;
        self.instances[idx].singbox_pid = None;
        self.save()?;
        Ok(())
    }

    pub fn list_instances(&self) -> &[Instance] {
        &self.instances
    }

    /// 校验实例存在且 Running，返回其 API 端口（供锁外探测）。
    pub fn prepare_test(&self, name: &str) -> Result<u16> {
        let inst = self
            .find_instance(name)
            .with_context(|| format!("实例 '{}' 不存在", name))?;

        if inst.status != InstanceStatus::Running {
            bail!(
                "实例 '{}' 当前状态为 {:?}，请先启动后再测试",
                name,
                inst.status
            );
        }
        Ok(inst.port)
    }

    /// 对运行中的实例执行真实免费模型最小请求，判断免费额度是否可用（F2）。
    /// 启用 401 门禁后需带实例密钥（Authorization: Bearer <密码>），否则自检会 401。
    pub fn test_instance(&self, name: &str) -> Result<TestResult> {
        let port = self.prepare_test(name)?;
        let auth = self.find_instance(name).map(|i| {
            if i.password.is_empty() {
                crate::config::Config::effective_default_password()
            } else {
                i.password.clone()
            }
        });
        Ok(probe_free_completion(name, port, auth.as_deref()))
    }

    #[allow(dead_code)]
    pub fn find_instance(&self, name: &str) -> Option<&Instance> {
        self.instances.iter().find(|i| i.name == name)
    }

    #[allow(dead_code)]
    pub fn find_instance_mut(&mut self, name: &str) -> Option<&mut Instance> {
        self.instances.iter_mut().find(|i| i.name == name)
    }

    /// 设置实例是否加入统一网关池（join_gateway）。
    /// 不持久化，由调用方决定何时 save_state。
    pub fn set_join_gateway(&mut self, name: &str, join: bool) -> Result<()> {
        let inst = self
            .instances
            .iter_mut()
            .find(|i| i.name == name)
            .context("实例不存在")?;
        inst.join_gateway = join;
        Ok(())
    }

    fn save(&self) -> Result<()> {
        if !self.persist_state {
            return Ok(());
        }
        let data = serde_json::to_string_pretty(&self.instances).context("序列化实例失败")?;
        fs::write(&self.config_path, data).context("写入实例文件失败")?;
        Ok(())
    }

    pub fn save_state(&self) -> Result<()> {
        self.save()
    }

    pub fn load(&mut self) -> Result<()> {
        if self.config_path.exists() {
            let data = fs::read_to_string(&self.config_path).context("读取实例文件失败")?;
            self.instances = serde_json::from_str(&data).context("解析实例文件失败")?;
        }
        Ok(())
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TestResult {
    pub name: String,
    pub port: u16,
    pub ok: bool,
    pub status_code: Option<u16>,
    pub model_count: Option<usize>,
    pub message: String,
    pub latency_ms: u64,
}

/// 使用临时 manager 并行启动实例（不持有共享 InstanceManager 锁）。
/// 启动完成后由调用方通过 apply_start_result 回写结果。
pub fn start_instance_process(
    instance: Instance,
    binary_dir: &PathBuf,
    runtime_dir: &PathBuf,
) -> std::result::Result<Instance, String> {
    let mut manager = InstanceManager::new_ephemeral(binary_dir.clone(), runtime_dir.clone());
    let name = instance.name.clone();
    manager.instances.push(instance);
    manager
        .start_instance_inner(&name)
        .map_err(|error| error.to_string())?;
    manager
        .instances
        .into_iter()
        .next()
        .ok_or_else(|| "启动后实例消失".to_string())
}

/// 对指定端口探测 `GET /v1/models`（可在锁外 / spawn_blocking 中调用）。
pub fn probe_models(name: &str, port: u16, auth_token: Option<&str>) -> TestResult {
    let start = Instant::now();
    match http_get_json(port, "/v1/models", Duration::from_secs(10), auth_token) {
        Ok((status, body)) => {
            let latency_ms = start.elapsed().as_millis() as u64;
            if !(200..300).contains(&status) {
                return TestResult {
                    name: name.to_string(),
                    port,
                    ok: false,
                    status_code: Some(status),
                    model_count: None,
                    message: format!("HTTP {}，响应: {}", status, truncate(&body, 200)),
                    latency_ms,
                };
            }
            let model_count = count_models_in_body(&body);
            TestResult {
                name: name.to_string(),
                port,
                ok: true,
                status_code: Some(status),
                model_count,
                message: match model_count {
                    Some(n) => format!("models 正常，共 {} 个模型", n),
                    None => "models 接口返回成功（未能解析模型数量）".to_string(),
                },
                latency_ms,
            }
        }
        Err(e) => {
            let latency_ms = start.elapsed().as_millis() as u64;
            TestResult {
                name: name.to_string(),
                port,
                ok: false,
                status_code: None,
                model_count: None,
                message: format!("请求失败: {}", e),
                latency_ms,
            }
        }
    }
}

// ======================== F2 免费额度实测健康检查 ========================

/// 实测请求的 2xx 判定：仅当响应带非空 choices 才算可用。
pub(crate) fn is_probe_completion_success(status: u16, body: &str) -> bool {
    if !(200..300).contains(&status) {
        return false;
    }
    let Ok(value) = serde_json::from_str::<serde_json::Value>(body) else {
        return false;
    };
    value
        .get("choices")
        .and_then(|choices| choices.as_array())
        .is_some_and(|choices| !choices.is_empty())
}

fn is_probe_free_model(model: &str) -> bool {
    let id = model.trim().to_ascii_lowercase();
    id.contains("-free")
        || id == "big-pickle"
        || matches!(
            id.as_str(),
            "deepseek-v4-flash"
                | "mimo-v2.5"
                | "ling-3.0-flash"
                | "nemotron-3-ultra"
                | "north-mini-code"
                | "laguna-s-2.1"
        )
}

fn select_probe_free_model(body: &str) -> Option<String> {
    let value: serde_json::Value = serde_json::from_str(body).ok()?;
    let data = value.get("data")?.as_array()?;
    let mut first = None;
    for item in data {
        let Some(id) = item.get("id").and_then(|v| v.as_str()) else {
            continue;
        };
        if !is_probe_free_model(id) {
            continue;
        }
        if first.is_none() {
            first = Some(id.to_string());
        }
        if id.contains("-free") || id.eq_ignore_ascii_case("big-pickle") {
            return Some(id.to_string());
        }
    }
    first
}

/// 先验证模型目录，再发送一个仅生成 1 token 的免费模型请求。
/// 返回的 status/body 与普通 HTTP 探测一致，供扫描器复用并保持错误分类一致。
pub(crate) fn probe_free_completion_response(
    port: u16,
    auth_token: Option<&str>,
    timeout: Duration,
) -> Result<(u16, String)> {
    if timeout.is_zero() {
        bail!("probe timeout must be positive");
    }
    // Keep the models lookup and completion request within the caller's total
    // budget instead of giving each phase an independent minimum timeout.
    let mut models_timeout = (timeout / 2).min(Duration::from_secs(4));
    if models_timeout.is_zero() {
        models_timeout = timeout;
    }
    let (models_status, models_body) =
        http_get_json(port, "/v1/models", models_timeout, auth_token)?;
    if !(200..300).contains(&models_status) {
        return Ok((models_status, models_body));
    }

    let Some(model) = select_probe_free_model(&models_body) else {
        return Ok((503, "models 接口成功，但没有可测试的免费模型".to_string()));
    };

    let remaining = timeout.saturating_sub(models_timeout);
    let request_body = serde_json::json!({
        "model": model,
        "messages": [{"role": "user", "content": "Reply with OK"}],
        "max_tokens": 1,
        "stream": false
    });
    let request_body = serde_json::to_string(&request_body).context("生成免费模型测试请求失败")?;
    http_post_json(
        port,
        "/v1/chat/completions",
        &request_body,
        remaining,
        auth_token,
    )
}

/// 对当前节点执行真实的免费模型最小请求。
/// 仅 models 接口成功不能证明上游可用，因此扫描和实例测试都必须经过这里。
pub fn probe_free_completion(name: &str, port: u16, auth_token: Option<&str>) -> TestResult {
    let start = Instant::now();
    match probe_free_completion_response(port, auth_token, Duration::from_secs(10)) {
        Ok((status, body)) => TestResult {
            name: name.to_string(),
            port,
            ok: is_probe_completion_success(status, &body),
            status_code: Some(status),
            model_count: None,
            message: if is_probe_completion_success(status, &body) {
                "免费模型最小请求成功".to_string()
            } else {
                format!("免费模型请求 HTTP {}：{}", status, truncate(&body, 240))
            },
            latency_ms: start.elapsed().as_millis() as u64,
        },
        Err(e) => TestResult {
            name: name.to_string(),
            port,
            ok: false,
            status_code: None,
            model_count: None,
            message: format!("免费模型请求失败: {}", e),
            latency_ms: start.elapsed().as_millis() as u64,
        },
    }
}

fn truncate(s: &str, max: usize) -> String {
    let mut t: String = s.chars().take(max).collect();
    if s.chars().count() > max {
        t.push('…');
    }
    t
}

fn count_models_in_body(body: &str) -> Option<usize> {
    let v: serde_json::Value = serde_json::from_str(body).ok()?;
    if let Some(arr) = v.get("data").and_then(|d| d.as_array()) {
        return Some(arr.len());
    }
    if let Some(arr) = v.as_array() {
        return Some(arr.len());
    }
    None
}

/// 向本机实例发简单 HTTP/1.1 GET，返回 (status, body)。
/// `auth_token` 非空时附带 `Authorization: Bearer <token>`（用于启用 401 门禁后的自检）。
pub(crate) fn http_get_json(
    port: u16,
    path: &str,
    timeout: Duration,
    auth_token: Option<&str>,
) -> Result<(u16, String)> {
    let addr = format!("127.0.0.1:{}", port);
    let mut stream = TcpStream::connect(&addr).with_context(|| format!("无法连接 {}", addr))?;
    stream
        .set_read_timeout(Some(timeout))
        .context("设置读超时失败")?;
    stream
        .set_write_timeout(Some(Duration::from_secs(5)))
        .context("设置写超时失败")?;

    let auth_line = match auth_token.filter(|t| !t.is_empty()) {
        Some(t) => format!("\r\nAuthorization: Bearer {}", t),
        None => String::new(),
    };
    let req = format!(
        "GET {path} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\nAccept: application/json\r\nUser-Agent: opencode2api-manager/0.1{}\r\n\r\n",
        auth_line
    );
    stream
        .write_all(req.as_bytes())
        .context("发送 HTTP 请求失败")?;

    let mut buf = Vec::new();
    stream.read_to_end(&mut buf).context("读取 HTTP 响应失败")?;
    let raw = String::from_utf8_lossy(&buf);
    let (header, body) = raw
        .split_once("\r\n\r\n")
        .or_else(|| raw.split_once("\n\n"))
        .unwrap_or((raw.as_ref(), ""));

    let status = header
        .lines()
        .next()
        .and_then(|line| {
            // HTTP/1.1 200 OK
            line.split_whitespace().nth(1)?.parse::<u16>().ok()
        })
        .unwrap_or(0);

    Ok((status, body.trim().to_string()))
}

/// 向本机实例发简单 HTTP/1.1 POST（F2 免费模型实测用），返回 (status, body)。
pub(crate) fn http_post_json(
    port: u16,
    path: &str,
    body: &str,
    timeout: Duration,
    auth_token: Option<&str>,
) -> Result<(u16, String)> {
    let addr = format!("127.0.0.1:{}", port);
    let mut stream = TcpStream::connect(&addr).with_context(|| format!("无法连接 {}", addr))?;
    stream
        .set_read_timeout(Some(timeout))
        .context("设置读超时失败")?;
    stream
        .set_write_timeout(Some(Duration::from_secs(5)))
        .context("设置写超时失败")?;

    let auth_line = match auth_token.filter(|t| !t.is_empty()) {
        Some(t) => format!("Authorization: Bearer {}\r\n", t),
        None => String::new(),
    };
    let req = format!(
        "POST {path} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\nAccept: application/json\r\nContent-Type: application/json\r\nContent-Length: {}\r\n{}User-Agent: opencode2api-manager/0.1\r\n\r\n{}",
        body.as_bytes().len(),
        auth_line,
        body
    );
    stream
        .write_all(req.as_bytes())
        .context("发送 HTTP POST 请求失败")?;

    let mut buf = Vec::new();
    stream
        .read_to_end(&mut buf)
        .context("读取 HTTP POST 响应失败")?;
    let raw = String::from_utf8_lossy(&buf);
    let (header, resp_body) = raw
        .split_once("\r\n\r\n")
        .or_else(|| raw.split_once("\n\n"))
        .unwrap_or((raw.as_ref(), ""));

    let status = header
        .lines()
        .next()
        .and_then(|line| line.split_whitespace().nth(1)?.parse::<u16>().ok())
        .unwrap_or(0);

    Ok((status, resp_body.trim().to_string()))
}

/// 向本机实例发简单 HTTP/1.1 DELETE（统计重置等管理操作用），返回 (status, body)。
/// `auth_token` 非空时附带 `Authorization: Bearer <token>`（apiKeyAuth 门禁）。
pub(crate) fn http_delete_json(
    port: u16,
    path: &str,
    timeout: Duration,
    auth_token: Option<&str>,
) -> Result<(u16, String)> {
    let addr = format!("127.0.0.1:{}", port);
    let mut stream = TcpStream::connect(&addr).with_context(|| format!("无法连接 {}", addr))?;
    stream
        .set_read_timeout(Some(timeout))
        .context("设置读超时失败")?;
    stream
        .set_write_timeout(Some(Duration::from_secs(5)))
        .context("设置写超时失败")?;

    let auth_line = match auth_token.filter(|t| !t.is_empty()) {
        Some(t) => format!("Authorization: Bearer {}\r\n", t),
        None => String::new(),
    };
    let req = format!(
        "DELETE {path} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\nAccept: application/json\r\n{}User-Agent: opencode2api-manager/0.1\r\n\r\n",
        auth_line
    );
    stream
        .write_all(req.as_bytes())
        .context("发送 HTTP DELETE 请求失败")?;

    let mut buf = Vec::new();
    stream
        .read_to_end(&mut buf)
        .context("读取 HTTP DELETE 响应失败")?;
    let raw = String::from_utf8_lossy(&buf);
    let (header, resp_body) = raw
        .split_once("\r\n\r\n")
        .or_else(|| raw.split_once("\n\n"))
        .unwrap_or((raw.as_ref(), ""));

    let status = header
        .lines()
        .next()
        .and_then(|line| line.split_whitespace().nth(1)?.parse::<u16>().ok())
        .unwrap_or(0);

    Ok((status, resp_body.trim().to_string()))
}

/// 等待本地 TCP 端口可连接
pub(crate) fn wait_for_port(port: u16, timeout: Duration) -> bool {
    let addr = format!("127.0.0.1:{}", port);
    let start = Instant::now();
    while start.elapsed() < timeout {
        if TcpStream::connect(&addr).is_ok() {
            return true;
        }
        thread::sleep(Duration::from_millis(200));
    }
    false
}

/// 校验本地端口空闲（用于并发扫描端口对分配）。
pub(crate) fn ensure_port_available(port: u16) -> Result<()> {
    let addr = format!("127.0.0.1:{}", port);
    TcpListener::bind(&addr)
        .map(|_| ())
        .with_context(|| format!("本地端口 {} 已被占用", port))
}

/// 快速判断本地端口是否空闲：能 bind 就说明没被监听。
/// 相比 wait_for_port 不需要等 200ms，适合端口建议/校验这种高频调用。
pub(crate) fn is_port_free(port: u16) -> bool {
    std::net::TcpListener::bind(("127.0.0.1", port)).is_ok()
}
/// 按 PID 终止进程（Windows 用 taskkill，其他平台用 sysinfo）
pub fn kill_process(pid: u32) -> Result<()> {
    #[cfg(windows)]
    {
        let output = no_window(&mut Command::new("taskkill"))
            .args(["/PID", &pid.to_string(), "/F"])
            .output()
            .context("执行 taskkill 失败")?;
        if output.status.success() {
            Ok(())
        } else {
            bail!(
                "终止进程 {} 失败: {}",
                pid,
                String::from_utf8_lossy(&output.stderr)
            );
        }
    }
    #[cfg(not(windows))]
    {
        use sysinfo::{Pid, System};
        let mut sys = System::new_all();
        sys.refresh_processes();
        if let Some(p) = sys.process(Pid::from_u32(pid)) {
            p.kill();
            Ok(())
        } else {
            bail!("进程 {} 不存在", pid);
        }
    }
}

impl InstanceManager {
    /// 状态校正：磁盘上标记 Running/Starting 的实例，若对应进程已不存在，
    /// 则修正为 Stopped 并清空 PID（防"僵尸运行中"状态）。
    /// 仅在发生变更时写盘。启动时与 list_instances 轮询时调用。
    ///
    /// 实现说明：一次枚举整张系统进程表，然后批量内存查找。
    /// 避免对每个实例 spawn 一次 tasklist 子进程（实例多时会卡死 UI，
    /// 例如 5000 个实例 × 串行子进程 ≈ 分钟级）。
    pub fn reconcile_states(&mut self) -> Result<()> {
        // 先收集待检查的 (下标, PID)，避免迭代时同时持有可变借用
        let to_check: Vec<(usize, u32)> = self
            .instances
            .iter()
            .enumerate()
            .filter(|(_, i)| matches!(i.status, InstanceStatus::Running | InstanceStatus::Starting))
            .filter_map(|(idx, i)| i.pid.map(|p| (idx, p)))
            .collect();
        if to_check.is_empty() {
            return Ok(());
        }

        // 一次性枚举系统进程
        use sysinfo::{Pid, System};
        let mut sys = System::new_all();
        sys.refresh_processes();

        let mut changed = false;
        for (idx, pid) in to_check {
            if sys.process(Pid::from_u32(pid)).is_none() {
                let inst = &mut self.instances[idx];
                inst.status = InstanceStatus::Stopped;
                inst.pid = None;
                inst.singbox_pid = None;
                changed = true;
            }
        }
        if changed {
            self.save()?;
        }
        Ok(())
    }

    /// 只校正指定名称的实例（进程存活检查），返回这些实例的最新状态。
    /// 仍是一次枚举进程表（快），仅更新传入名称的实例；
    /// 供前端手动刷新时分批调用，进度由前端按返回数量累计。
    pub fn reconcile_batch(&mut self, names: &[String]) -> Result<Vec<Instance>> {
        let to_check: Vec<(usize, u32)> = self
            .instances
            .iter()
            .enumerate()
            .filter(|(_, i)| names.iter().any(|n| n == &i.name))
            .filter(|(_, i)| matches!(i.status, InstanceStatus::Running | InstanceStatus::Starting))
            .filter_map(|(idx, i)| i.pid.map(|p| (idx, p)))
            .collect();

        if !to_check.is_empty() {
            use sysinfo::{Pid, System};
            let mut sys = System::new_all();
            sys.refresh_processes();

            let mut changed = false;
            for (idx, pid) in to_check {
                if sys.process(Pid::from_u32(pid)).is_none() {
                    let inst = &mut self.instances[idx];
                    inst.status = InstanceStatus::Stopped;
                    inst.pid = None;
                    inst.singbox_pid = None;
                    changed = true;
                }
            }
            if changed {
                self.save()?;
            }
        }

        Ok(self
            .instances
            .iter()
            .filter(|i| names.iter().any(|n| n == &i.name))
            .cloned()
            .collect())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::env;

    fn temp_dir(name: &str) -> PathBuf {
        let dir = env::temp_dir().join(format!("opencode2api-manager-test-{}", name));
        fs::create_dir_all(&dir).ok();
        dir
    }

    fn new_manager(name: &str) -> InstanceManager {
        let dir = temp_dir(name);
        InstanceManager::new(
            dir.join("instances.json"),
            dir.join("bin"),
            dir.join("runtime"),
        )
    }

    // ======================== F2 免费模型选择 ========================

    #[test]
    fn test_select_probe_free_model_prefers_free_suffix() {
        let body =
            r#"{"data":[{"id":"gpt-4o"},{"id":"deepseek-v4-flash-free"},{"id":"big-pickle"}]}"#;
        let got = select_probe_free_model(body);
        assert_eq!(got.as_deref(), Some("deepseek-v4-flash-free"));
    }

    #[test]
    fn test_select_probe_free_model_falls_back_to_known_free() {
        let body = r#"{"data":[{"id":"gpt-4o"},{"id":"big-pickle"}]}"#;
        let got = select_probe_free_model(body);
        assert_eq!(got.as_deref(), Some("big-pickle"));
    }

    #[test]
    fn test_select_probe_free_model_matches_contains_free() {
        // 名称任意位置包含 "-free"（不止后缀）也应识别为免费
        let body = r#"{"data":[{"id":"gpt-4o"},{"id":"x-free-7b-instruct"},{"id":"gpt-4-turbo"}]}"#;
        let got = select_probe_free_model(body);
        assert_eq!(got.as_deref(), Some("x-free-7b-instruct"));
    }

    #[test]
    fn test_select_probe_free_model_none_when_no_free() {
        let body = r#"{"data":[{"id":"gpt-4o"},{"id":"gpt-4-turbo"}]}"#;
        assert!(select_probe_free_model(body).is_none());
    }

    #[test]
    fn test_is_probe_completion_success_requires_choices() {
        assert!(is_probe_completion_success(
            200,
            r#"{"choices":[{"index":0}]}"#
        ));
        assert!(!is_probe_completion_success(200, r#"{"choices":[]}"#));
        assert!(!is_probe_completion_success(
            200,
            r#"{"error":"rate limited"}"#
        ));
        assert!(!is_probe_completion_success(
            429,
            r#"{"choices":[{"index":0}]}"#
        ));
        assert!(!is_probe_completion_success(503, ""));
    }

    #[test]
    fn test_add_instance() {
        let mut manager = new_manager("add");
        manager
            .add_instance(
                "user1".to_string(),
                8088,
                "新加坡 G1".to_string(),
                "".to_string(),
                "".to_string(),
            )
            .unwrap();
        assert_eq!(manager.instances.len(), 1);
        assert_eq!(manager.instances[0].name, "user1");
        assert_eq!(manager.instances[0].port, 8088);
        assert_eq!(manager.instances[0].singbox_port, 18088);
        fs::remove_dir_all(temp_dir("add")).ok();
    }

    #[test]
    fn test_add_duplicate() {
        let mut manager = new_manager("dup");
        manager
            .add_instance(
                "a".to_string(),
                8088,
                "n".to_string(),
                "".to_string(),
                "".to_string(),
            )
            .unwrap();
        let r = manager.add_instance(
            "a".to_string(),
            8089,
            "n".to_string(),
            "".to_string(),
            "".to_string(),
        );
        assert!(r.is_err());
        fs::remove_dir_all(temp_dir("dup")).ok();
    }

    #[test]
    fn test_add_duplicate_port() {
        let mut manager = new_manager("dupport");
        manager
            .add_instance(
                "a".to_string(),
                8088,
                "n".to_string(),
                "".to_string(),
                "".to_string(),
            )
            .unwrap();
        let r = manager.add_instance(
            "b".to_string(),
            8088,
            "n".to_string(),
            "".to_string(),
            "".to_string(),
        );
        assert!(r.is_err());
        fs::remove_dir_all(temp_dir("dupport")).ok();
    }

    #[test]
    fn test_start_not_found() {
        let mut manager = new_manager("startnf");
        let r = manager.start_instance_inner("nobody");
        assert!(r.is_err());
        fs::remove_dir_all(temp_dir("startnf")).ok();
    }

    #[test]
    fn test_mark_starting_and_apply_result() {
        let mut manager = new_manager("markstart");
        manager
            .add_instance(
                "u1".to_string(),
                8088,
                "n".to_string(),
                "".to_string(),
                "".to_string(),
            )
            .unwrap();

        // mark_starting：状态 → Starting，返回快照
        let snap = manager.mark_starting("u1").unwrap();
        assert_eq!(snap.status, InstanceStatus::Starting);
        assert_eq!(manager.instances[0].status, InstanceStatus::Starting);

        // 忙状态不能重复标记
        assert!(manager.mark_starting("u1").is_err());

        // apply_start_result 成功 → Running
        let mut done = snap.clone();
        done.pid = Some(4242);
        done.singbox_pid = Some(4243);
        manager.apply_start_result("u1", Ok(done)).unwrap();
        assert_eq!(manager.instances[0].status, InstanceStatus::Running);
        assert_eq!(manager.instances[0].pid, Some(4242));

        // apply_start_result 失败 → Error
        assert!(manager
            .apply_start_result("u1", Err("启动失败".to_string()))
            .is_err());
        assert!(matches!(
            manager.instances[0].status,
            InstanceStatus::Error(_)
        ));
        assert_eq!(manager.instances[0].pid, None);
        fs::remove_dir_all(temp_dir("markstart")).ok();
    }

    #[test]
    fn test_prepare_stop_and_finish_stop() {
        let mut manager = new_manager("prepstop");
        manager
            .add_instance(
                "u1".to_string(),
                8089,
                "n".to_string(),
                "".to_string(),
                "".to_string(),
            )
            .unwrap();
        manager.instances[0].status = InstanceStatus::Running;
        manager.instances[0].pid = Some(9999);
        manager.instances[0].singbox_pid = Some(8888);

        let pids = manager.prepare_stop("u1").unwrap();
        assert_eq!(pids, (Some(9999), Some(8888)));
        assert_eq!(manager.instances[0].status, InstanceStatus::Stopping);

        manager.finish_stop("u1").unwrap();
        assert_eq!(manager.instances[0].status, InstanceStatus::Stopped);
        assert_eq!(manager.instances[0].pid, None);
        assert_eq!(manager.instances[0].singbox_pid, None);
        fs::remove_dir_all(temp_dir("prepstop")).ok();
    }

    #[test]
    fn test_reconcile_marks_dead_running_as_stopped() {
        let mut manager = new_manager("reconcile");
        manager
            .add_instance(
                "u1".to_string(),
                8088,
                "n".to_string(),
                "".to_string(),
                "".to_string(),
            )
            .unwrap();
        // 构造"进程已死但状态残留 Running"的僵尸状态
        manager.instances[0].status = InstanceStatus::Running;
        manager.instances[0].pid = Some(u32::MAX); // 必然不存在的 PID
        manager.instances[0].singbox_pid = Some(u32::MAX);
        manager.reconcile_states().unwrap();
        assert_eq!(manager.instances[0].status, InstanceStatus::Stopped);
        assert_eq!(manager.instances[0].pid, None);
        assert_eq!(manager.instances[0].singbox_pid, None);
        fs::remove_dir_all(temp_dir("reconcile")).ok();
    }

    #[test]
    fn test_reconcile_keeps_alive_running() {
        let mut manager = new_manager("reconcile_alive");
        manager
            .add_instance(
                "u1".to_string(),
                8088,
                "n".to_string(),
                "".to_string(),
                "".to_string(),
            )
            .unwrap();
        // 当前测试进程自身是活着的，Running 应保留
        manager.instances[0].status = InstanceStatus::Running;
        manager.instances[0].pid = Some(std::process::id());
        manager.reconcile_states().unwrap();
        assert_eq!(manager.instances[0].status, InstanceStatus::Running);
        assert!(manager.instances[0].pid.is_some());
        fs::remove_dir_all(temp_dir("reconcile_alive")).ok();
    }

    #[test]
    fn test_reconcile_leaves_stopped_untouched() {
        let mut manager = new_manager("reconcile_stopped");
        manager
            .add_instance(
                "u1".to_string(),
                8088,
                "n".to_string(),
                "".to_string(),
                "".to_string(),
            )
            .unwrap();
        manager.instances[0].status = InstanceStatus::Stopped;
        manager.instances[0].pid = Some(u32::MAX); // 已停止状态不校验进程
        manager.reconcile_states().unwrap();
        assert_eq!(manager.instances[0].status, InstanceStatus::Stopped);
        // 已停止实例不应被误改
        assert!(manager.instances[0].pid.is_some());
        fs::remove_dir_all(temp_dir("reconcile_stopped")).ok();
    }

    #[test]
    fn test_reconcile_batch_only_updates_named_instances() {
        let mut manager = new_manager("reconcile_batch");
        manager
            .add_instance(
                "dead".to_string(),
                8088,
                "n".to_string(),
                "".to_string(),
                "".to_string(),
            )
            .unwrap();
        manager
            .add_instance(
                "alive".to_string(),
                8089,
                "n".to_string(),
                "".to_string(),
                "".to_string(),
            )
            .unwrap();
        manager
            .add_instance(
                "skip".to_string(),
                8090,
                "n".to_string(),
                "".to_string(),
                "".to_string(),
            )
            .unwrap();
        // dead：进程必然不存在，应被校正为 Stopped
        manager.instances[0].status = InstanceStatus::Running;
        manager.instances[0].pid = Some(u32::MAX);
        // alive：当前测试进程活着，Running 应保留
        manager.instances[1].status = InstanceStatus::Running;
        manager.instances[1].pid = Some(std::process::id());
        // skip：僵尸状态但不传入名字，不应被校正
        manager.instances[2].status = InstanceStatus::Running;
        manager.instances[2].pid = Some(u32::MAX);

        let updated = manager
            .reconcile_batch(&["dead".to_string(), "alive".to_string()])
            .unwrap();
        assert_eq!(manager.instances[0].status, InstanceStatus::Stopped);
        assert_eq!(manager.instances[1].status, InstanceStatus::Running);
        // 未传入的实例保持原状（仍是僵尸 Running）
        assert_eq!(manager.instances[2].status, InstanceStatus::Running);
        // 返回的实例数与传入一致，且按原顺序
        assert_eq!(updated.len(), 2);
        assert_eq!(updated[0].name, "dead");
        assert_eq!(updated[1].name, "alive");
        fs::remove_dir_all(temp_dir("reconcile_batch")).ok();
    }

    #[test]
    fn test_is_port_free_reports_bound_port() {
        let listener = std::net::TcpListener::bind(("127.0.0.1", 0)).unwrap();
        let port = listener.local_addr().unwrap().port();
        assert!(!is_port_free(port));
        drop(listener);
        assert!(is_port_free(port));
    }
}
