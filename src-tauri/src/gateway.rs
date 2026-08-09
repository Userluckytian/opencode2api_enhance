use crate::config::Config;
use crate::instance::{no_window, Instance, InstanceStatus};
use crate::opencode_cfg;
use anyhow::{Context, Result};
use serde::Serialize;
use std::fs;
use std::io::{Read, Write};
use std::net::TcpStream;
use std::path::PathBuf;
use std::process::{Child, Command, Stdio};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::Duration;

/// 统一网关端口回退默认：debug 构建（tauri dev）用 21080 段（与 main 18080、web 22080 隔离），
/// release 构建用生产 18080。实际生效端口优先取 config.gateway_port（Config::effective_gateway_port）。
#[cfg(debug_assertions)]
pub const UNIFIED_GATEWAY_PORT: u16 = 21080;
#[cfg(not(debug_assertions))]
pub const UNIFIED_GATEWAY_PORT: u16 = 18080;
#[derive(Debug, Clone, Serialize)]
pub struct GatewayStatus {
    pub running: bool,
    pub address: String,
    pub port: u16,
    pub api_key: String,
    pub running_instances: usize,
    pub total_instances: usize,
    pub message: String,
    pub route_mode: String,
    pub free_models: Vec<String>,
    pub free_models_updated_at: Option<u64>,
    pub free_models_loading: bool,
    pub free_models_error: Option<String>,
}

#[derive(Default)]
struct ModelCatalog {
    models: Vec<String>,
    updated_at: Option<u64>,
    last_attempt: Option<u64>,
    loading: bool,
    error: Option<String>,
}

pub struct GatewayManager {
    binary_dir: PathBuf,
    runtime_dir: PathBuf,
    config_path: PathBuf,
    password: String,
    port: u16,
    child: Option<Child>,
    ports: Vec<u16>,
    route_mode: String,
    last_error: Option<String>,
    restart_not_before: Option<std::time::Instant>,
    model_catalog: Arc<Mutex<ModelCatalog>>,
}
impl GatewayManager {
    pub fn new(binary_dir: PathBuf, runtime_dir: PathBuf) -> Self {
        let gateway_dir = runtime_dir.join("_unified-gateway");
        Self {
            binary_dir,
            runtime_dir,
            config_path: gateway_dir.join("opencode2api.json"),
            password: Config::effective_gateway_key(),
            port: Config::effective_gateway_port(),
            child: None,
            ports: Vec::new(),
            route_mode: "smart".to_string(),
            last_error: None,
            restart_not_before: None,
            model_catalog: Arc::new(Mutex::new(ModelCatalog::default())),
        }
    }

    /// 设置路由模式（failover/round_robin），下次 sync 时写入网关配置。
    pub fn set_route_mode(&mut self, mode: &str) {
        self.route_mode = mode.to_string();
    }

    fn gateway_dir(&self) -> PathBuf {
        self.runtime_dir.join("_unified-gateway")
    }

    fn reap_child(&mut self) -> bool {
        let Some(child) = self.child.as_mut() else {
            return false;
        };
        match child.try_wait() {
            Ok(None) => true,
            Ok(Some(status)) => {
                self.last_error = Some(format!("统一网关已退出: {}", status));
                self.child = None;
                self.restart_not_before = Some(std::time::Instant::now() + Duration::from_secs(2));
                false
            }
            Err(e) => {
                self.last_error = Some(format!("检查统一网关状态失败: {}", e));
                self.restart_not_before = Some(std::time::Instant::now() + Duration::from_secs(2));
                false
            }
        }
    }

    fn stop_child(&mut self) {
        if let Some(mut child) = self.child.take() {
            let _ = child.kill();
            let _ = child.wait();
        }
        self.restart_not_before = None;
    }

    fn start_child(&mut self) -> Result<()> {
        if self
            .restart_not_before
            .is_some_and(|deadline| std::time::Instant::now() < deadline)
        {
            return Ok(());
        }
        fs::create_dir_all(self.gateway_dir()).context("创建统一网关目录失败")?;
        let stdout = fs::File::create(self.gateway_dir().join("opencode2api.out.log"))?;
        let stderr = fs::File::create(self.gateway_dir().join("opencode2api.err.log"))?;
        let exe = crate::instance::resolve_platform_bin(&self.binary_dir, "opencode2api");
        if !exe.exists() {
            anyhow::bail!("未找到统一网关代理程序: {}", self.binary_dir.display());
        }

        // A 化（关键）：只传 A 的 main.go 支持的 flag。
        // B 版本传 -force-free/-free-usage-file 会导致 A 的 go 进程 os.Exit(2) 秒退。
        let child = no_window(&mut Command::new(exe))
            // 工作目录设为网关专属目录：Go 核心把 stats.json 写入当前工作目录，
            // 不设置则落到应用 exe 目录，统计界面（只扫 runtime/）读不到。
            .current_dir(self.gateway_dir())
            .arg("-port")
            .arg(self.port.to_string())
            .arg("-config")
            .arg(&self.config_path)
            .arg("-password")
            .arg(&self.password)
            .arg("-gateway")
            .arg("-log-level")
            .arg("warn")
            .stdout(Stdio::from(stdout))
            .stderr(Stdio::from(stderr))
            .spawn()
            .context("启动统一 API 网关失败")?;
        self.child = Some(child);
        self.restart_not_before = None;
        self.last_error = None;
        Ok(())
    }
    fn refresh_models_async(&self) {
        let Ok(mut catalog) = self.model_catalog.lock() else {
            return;
        };
        let now = unix_seconds();
        if catalog.loading
            || catalog
                .last_attempt
                .is_some_and(|last_attempt| now.saturating_sub(last_attempt) < 10)
            || catalog
                .updated_at
                .is_some_and(|updated_at| now.saturating_sub(updated_at) < 60)
        {
            return;
        }
        catalog.loading = true;
        catalog.last_attempt = Some(now);
        catalog.error = None;
        let target = Arc::clone(&self.model_catalog);
        let password = self.password.clone();
        let port = self.port;
        thread::spawn(move || {
            let result = fetch_gateway_models(port, &password);
            if let Ok(mut catalog) = target.lock() {
                catalog.loading = false;
                match result {
                    Ok(models) => {
                        catalog.models = models;
                        catalog.updated_at = Some(unix_seconds());
                        catalog.error = None;
                    }
                    Err(error) => {
                        catalog.error = Some(error.to_string());
                    }
                }
            }
        });
    }

    /// 同步网关：只把「运行中且已入池（join_gateway=true）」的实例加入池。
    /// 未入池实例保持独享访问，不受网关影响。
    pub fn sync(&mut self, instances: &[Instance]) -> Result<()> {
        // 每次同步都重读生效密钥/端口：config_set 改 gateway_key/gateway_port 后
        // 走 stop()+sync() 重建网关，此处刷新才能让新值真正传给 Go 进程并反映到 status()。
        self.password = Config::effective_gateway_key();
        self.port = Config::effective_gateway_port();
        let members: Vec<&Instance> = instances
            .iter()
            .filter(|instance| instance.status == InstanceStatus::Running && instance.join_gateway)
            .collect();
        let ports: Vec<u16> = members.iter().map(|i| i.singbox_port).collect();
        // 端口 → 实例名映射（供流式前缀显示「🤖 实例名 · 模型」）
        let port_names: Vec<(u16, String)> = members
            .iter()
            .map(|i| (i.singbox_port, i.name.clone()))
            .collect();

        self.reap_child();
        if ports.is_empty() {
            self.stop_child();
            self.ports.clear();
            if let Ok(mut catalog) = self.model_catalog.lock() {
                catalog.models.clear();
                catalog.updated_at = None;
                catalog.last_attempt = None;
                catalog.error = None;
                catalog.loading = false;
            }
            return Ok(());
        }

        let config =
            opencode_cfg::build_opencode_router_config(&ports, &port_names, &self.route_mode)?;
        fs::create_dir_all(self.gateway_dir()).context("创建统一网关目录失败")?;
        let changed = fs::read_to_string(&self.config_path)
            .map(|old| old != config)
            .unwrap_or(true);
        if changed {
            fs::write(&self.config_path, config).context("写入统一网关配置失败")?;
        }

        if self.ports != ports {
            self.ports = ports;
        }
        let running = self.reap_child();
        if changed && running {
            // 配置已变更且网关正在运行：Go 进程仅在启动时读取配置文件，
            // 运行时不会重载。重启进程让新配置立即生效（如节点前缀开关、
            // 路由超时区间等），避免「设置已保存但对话仍按旧值运行」。
            self.stop_child();
            self.start_child()?;
        } else if !running {
            self.start_child()?;
        }
        self.refresh_models_async();
        Ok(())
    }

    pub fn stop(&mut self) {
        self.stop_child();
        self.ports.clear();
    }

    pub fn status(&mut self, total_instances: usize) -> GatewayStatus {
        let mut running = self.reap_child();
        if !running && !self.ports.is_empty() {
            if let Err(e) = self.start_child() {
                self.last_error = Some(e.to_string());
            } else {
                running = true;
            }
        }
        if running {
            self.refresh_models_async();
        }
        let catalog = self
            .model_catalog
            .lock()
            .map(|catalog| {
                (
                    catalog.models.clone(),
                    catalog.updated_at,
                    catalog.loading,
                    catalog.error.clone(),
                )
            })
            .unwrap_or_default();
        let message = if self.ports.is_empty() {
            "暂无运行中的实例".to_string()
        } else if running {
            "已启动，遇到限流或节点错误会自动切换（failover）".to_string()
        } else {
            self.last_error
                .clone()
                .unwrap_or_else(|| "统一网关未启动".to_string())
        };
        GatewayStatus {
            running,
            address: format!("http://127.0.0.1:{}/v1", self.port),
            port: self.port,
            api_key: self.password.clone(),
            running_instances: self.ports.len(),
            total_instances,
            message,
            route_mode: self.route_mode.clone(),
            free_models: catalog.0,
            free_models_updated_at: catalog.1,
            free_models_loading: catalog.2,
            free_models_error: catalog.3,
        }
    }
}

fn unix_seconds() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|duration| duration.as_secs())
        .unwrap_or_default()
}

fn fetch_gateway_models(port: u16, password: &str) -> Result<Vec<String>> {
    let address = format!("127.0.0.1:{}", port);
    let mut stream = TcpStream::connect_timeout(
        &address.parse().context("invalid gateway address")?,
        Duration::from_secs(2),
    )?;
    stream.set_read_timeout(Some(Duration::from_secs(20)))?;
    stream.set_write_timeout(Some(Duration::from_secs(2)))?;
    let request = format!(
        "GET /v1/models HTTP/1.1\r\nHost: {}\r\nAuthorization: Bearer {}\r\nConnection: close\r\n\r\n",
        address, password
    );
    stream.write_all(request.as_bytes())?;
    let mut response = Vec::new();
    stream.read_to_end(&mut response)?;
    let response = String::from_utf8_lossy(&response);
    let (headers, body) = response
        .split_once("\r\n\r\n")
        .context("gateway models response has no body")?;
    let status = headers
        .lines()
        .next()
        .and_then(|line| line.split_whitespace().nth(1))
        .and_then(|code| code.parse::<u16>().ok())
        .context("gateway models response has no status")?;
    if !(200..300).contains(&status) {
        anyhow::bail!("gateway models returned HTTP {}", status);
    }
    let value: serde_json::Value =
        serde_json::from_str(body).context("invalid gateway models JSON")?;
    let mut models = value
        .get("data")
        .and_then(serde_json::Value::as_array)
        .map(|items| {
            items
                .iter()
                .filter_map(|item| item.get("id").and_then(serde_json::Value::as_str))
                .map(str::to_string)
                .collect::<Vec<_>>()
        })
        .unwrap_or_default();
    models.sort_unstable();
    models.dedup();
    Ok(models)
}

// 已移除：节点健康轮询（NodeHealth / node_health / fetch_node_health）。
// 坏池节点由 Go 侧 pickHealthyProxy 在路由层跳过，无需 Rust 轮询展示。

#[cfg(test)]
mod tests {
    use super::*;

    /// 网关端口三环境隔离：debug 构建（tauri dev）用 21080，release 构建用生产 18080
    #[test]
    fn gateway_port_isolated_by_build() {
        #[cfg(debug_assertions)]
        assert_eq!(UNIFIED_GATEWAY_PORT, 21080);
        #[cfg(not(debug_assertions))]
        assert_eq!(UNIFIED_GATEWAY_PORT, 18080);
    }
}
