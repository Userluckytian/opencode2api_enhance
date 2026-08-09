//! Headless HTTP 服务：与 Tauri 桌面共用同一套 *_core() 逻辑。
//!
//! 两种入口：
//! - 桌面模式：lib.rs `run()` 在 setup 中 spawn 本服务（127.0.0.1:19090），前端经它取数
//! - headless 模式：main.rs `serve` 子命令阻塞运行本服务（默认 127.0.0.1:19090，
//!   显式 `--bind 0.0.0.0` 才暴露到网络），同时托管打包后的前端静态文件（dist/），
//!   纯浏览器即可完成全部管理
//!
//! CORS 白名单：仅放行 Tauri 桌面前端来源（tauri://localhost）与同源请求，
//! 拒绝任意来源——防止恶意网页经浏览器驱动本机/内网管理 API。

use crate::commands;
use crate::core::AppCore;
use axum::extract::{Path, Query, State};
use axum::http::{header, StatusCode, Uri};
use axum::response::IntoResponse;
use axum::routing::{delete, get, post};
use axum::{Json, Router};
use serde::Deserialize;
use serde_json::json;
use std::sync::Arc;
use tower_http::cors::CorsLayer;

/// 编译期嵌入的前端静态资源（../dist，相对 src-tauri）。浏览器访问 19090
/// 与桌面 WebView 都从该嵌入资源提供前端，无需磁盘 dist/ 伴行（单文件自包含）。
#[derive(rust_embed::RustEmbed)]
#[folder = "../dist/"]
struct EmbeddedAssets;

/// 启动 headless HTTP 服务（阻塞）。bind_addr 形如 "127.0.0.1:19090" 或 "0.0.0.0:19090"。
pub async fn serve(bind_addr: &str, core: Arc<AppCore>) -> std::io::Result<()> {
    let app = build_router(core);
    let listener = tokio::net::TcpListener::bind(bind_addr).await?;
    println!("Headless 管理服务已启动: http://{}", bind_addr);
    axum::serve(listener, app).await
}

/// 构建 Router（桌面与 headless 共用同一路由表）
pub fn build_router(core: Arc<AppCore>) -> Router {
    Router::new()
        .route("/api/health", get(health_handler))
        .route("/api/instances", get(list_instances_handler))
        .route("/api/instances", post(add_instance_handler))
        .route("/api/instances/batch", post(batch_add_handler))
        .route("/api/instances/batch", delete(batch_delete_handler))
        .route("/api/instances/batch/start", post(batch_start_handler))
        .route("/api/instances/batch/stop", post(batch_stop_handler))
        .route("/api/instances/restart-pool", post(restart_pool_handler))
        .route("/api/instances/{name}", post(start_instance_handler))
        .route("/api/instances/{name}/stop", post(stop_instance_handler))
        .route("/api/instances/{name}/remove", post(remove_instance_handler))
        .route("/api/instances/{name}/test", post(test_instance_handler))
        .route("/api/gateway", get(gateway_status_handler))
        .route("/api/gateway/stop", post(gateway_stop_handler))
        .route("/api/gateway/route-mode", post(gateway_route_mode_handler))
        .route("/api/join-gateway", post(set_join_gateway_handler))
        .route("/api/config", get(config_get_handler))
        .route("/api/config/{key}", post(config_set_handler))
        .route("/api/stats", get(stats_handler))
        .route("/api/stats/reset", post(stats_reset_handler))
        .route("/api/call-log", get(call_log_handler))
        .route("/api/call-log/filtered", post(call_log_filtered_handler))
        .route("/api/call-log/aggregate", get(call_log_aggregate_handler))
        .route("/api/call-log/clear", post(clear_call_log_handler))
        .route("/api/nodes", get(nodes_handler))
        .route("/api/nodes/delete", post(node_delete_handler))
        .route("/api/nodes/delete-batch", post(node_delete_batch_handler))
        .route("/api/binaries", get(binaries_handler))
        .route("/api/port/suggest", get(port_suggest_handler))
        .route("/api/port/check", get(port_check_handler))
        .route("/api/scan/start", post(scan_start_handler))
        .route("/api/scan/status", get(scan_status_handler))
        .route("/api/scan/stop", post(scan_stop_handler))
        .route("/api/subscribe/preview", post(subscribe_preview_handler))
        .route("/api/subscribe/import", post(subscribe_import_handler))
        .route("/api/subscribe/import-pool", post(subscribe_import_pool_handler))
        .route("/api/health/check", post(health_check_handler))
        .route("/api/health/summary", get(health_summary_handler))
        .route("/api/autostart", get(autostart_get_handler))
        .route("/api/autostart", post(autostart_set_handler))
        .route("/api/export/call-log.csv", get(export_csv_handler))
        .route("/api/export/instances.json", get(export_instances_handler))
        .route("/api/export/stats.json", get(export_stats_handler))
        .route("/api/data-clean", post(data_clean_handler))
        // 其余路径由嵌入前端资源提供（SPA：未知路径回退 index.html）
        .fallback(embedded_assets)
        .layer(cors_layer())
        .with_state(core)
}

/// 从编译期嵌入资源（EmbeddedAssets）提供前端静态文件。
/// 支持 SPA 路由回退：未知路径（如 /settings 前端路由）返回 index.html。
async fn embedded_assets(uri: Uri) -> impl IntoResponse {
    let path = uri.path().trim_start_matches('/');
    // 实际返回的资源名：请求文件存在则用它，否则 SPA 回退 index.html。
    // MIME 必须基于实际资源名推断（若按请求路径，SPA 路由 /settings 回退
    // index.html 时会因未知扩展名返回 octet-stream 导致浏览器下载而非渲染）。
    let (served_name, asset) = if path.is_empty() || path == "index.html" {
        ("index.html", EmbeddedAssets::get("index.html"))
    } else {
        match EmbeddedAssets::get(path) {
            Some(f) => (path, Some(f)),
            None => ("index.html", EmbeddedAssets::get("index.html")),
        }
    };
    match asset {
        Some(f) => {
            let mime = mime_guess_from_path(served_name);
            (
                StatusCode::OK,
                [(header::CONTENT_TYPE, mime)],
                f.data.into_owned(),
            )
                .into_response()
        }
        None => (StatusCode::NOT_FOUND, "not found").into_response(),
    }
}

/// 依据扩展名推断 MIME 类型（嵌入资源无磁盘文件，无法依赖 mime_guess crate 的
/// 文件扩展名嗅探之外的逻辑；这里覆盖前端资源所需的核心类型）。
fn mime_guess_from_path(path: &str) -> &'static str {
    let ext = path.rsplit('.').next().unwrap_or("");
    match ext {
        "html" => "text/html; charset=utf-8",
        "js" | "mjs" => "text/javascript; charset=utf-8",
        "css" => "text/css; charset=utf-8",
        "json" => "application/json; charset=utf-8",
        "svg" => "image/svg+xml",
        "png" => "image/png",
        "jpg" | "jpeg" => "image/jpeg",
        "ico" => "image/x-icon",
        "woff" | "woff2" => "font/woff2",
        "ttf" => "font/ttf",
        "wasm" => "application/wasm",
        _ => "application/octet-stream",
    }
}

/// 构建 CORS 层：仅放行已知前端来源（Tauri 桌面前端 custom-protocol 来源
/// `tauri://localhost` 与 `http://tauri.localhost`）及同源请求。
/// 不设 `permissive()`：拒绝任意来源，防恶意网页驱动本机/内网管理 API。
fn cors_layer() -> CorsLayer {
    use tower_http::cors::AllowOrigin;
    CorsLayer::new()
        .allow_origin(AllowOrigin::predicate(|origin, _| {
            let o = origin.to_str().unwrap_or("");
            o == "tauri://localhost" || o == "http://tauri.localhost"
        }))
        .allow_methods([axum::http::Method::GET, axum::http::Method::POST, axum::http::Method::DELETE])
        .allow_headers(tower_http::cors::Any)
}

// ---------- Handler 实现 ----------

fn err(e: String) -> (StatusCode, String) {
    (StatusCode::BAD_REQUEST, e)
}

/// 在 spawn_blocking 中执行同步阻塞逻辑（fs/进程/网络 I/O），
/// 避免占住 tokio worker 线程。返回 Result 语义与 handler 一致。
async fn blocking<T, F>(f: F) -> Result<T, (StatusCode, String)>
where
    T: Send + 'static,
    F: FnOnce() -> Result<T, String> + Send + 'static,
{
    tokio::task::spawn_blocking(f)
        .await
        .map_err(|e| err(format!("任务执行失败: {}", e)))?
        .map_err(err)
}

fn to_json<T: serde::Serialize>(value: T) -> Json<serde_json::Value> {
    Json(serde_json::to_value(value).unwrap_or(json!({})))
}

async fn health_handler() -> impl IntoResponse {
    Json(json!({
        "ok": true,
        "service": "opencode2api-manager",
        "version": env!("CARGO_PKG_VERSION"),
    }))
}

async fn list_instances_handler(
    State(core): State<Arc<AppCore>>,
    Query(query): Query<RefreshQuery>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    if let Some(raw) = query.refresh {
        let names: Vec<String> = serde_json::from_str(&raw).unwrap_or_default();
        let core2 = core.clone();
        let result = tokio::task::spawn_blocking(move || commands::refresh_states_core(&core2, names))
            .await
            .map_err(|e| err(format!("刷新实例任务失败: {}", e)))?
            .map_err(err)?;
        return Ok(to_json(result));
    }
    Ok(to_json(commands::list_instances_core(&core).map_err(err)?))
}

#[derive(Deserialize)]
struct RefreshQuery {
    refresh: Option<String>,
}

#[derive(Deserialize)]
struct AddInstancePayload {
    name: String,
    port: u16,
    node: String,
    password: String,
}

async fn add_instance_handler(
    State(core): State<Arc<AppCore>>,
    Json(payload): Json<AddInstancePayload>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let instance = commands::add_instance_core(
        &core,
        payload.name,
        payload.port,
        payload.node,
        payload.password,
    )
    .map_err(err)?;
    Ok(to_json(instance))
}

async fn start_instance_handler(
    State(core): State<Arc<AppCore>>,
    Path(name): Path<String>,
) -> Result<impl IntoResponse, (StatusCode, String)> {
    let core2 = core.clone();
    tokio::task::spawn_blocking(move || commands::start_instance_core(&core2, &name))
        .await
        .map_err(|e| err(format!("启动实例任务失败: {}", e)))?
        .map_err(err)?;
    Ok(Json(json!({ "ok": true })))
}

async fn stop_instance_handler(
    State(core): State<Arc<AppCore>>,
    Path(name): Path<String>,
) -> Result<impl IntoResponse, (StatusCode, String)> {
    let core2 = core.clone();
    tokio::task::spawn_blocking(move || commands::stop_instance_core(&core2, &name))
        .await
        .map_err(|e| err(format!("停止实例任务失败: {}", e)))?
        .map_err(err)?;
    Ok(Json(json!({ "ok": true })))
}

async fn remove_instance_handler(
    State(core): State<Arc<AppCore>>,
    Path(name): Path<String>,
) -> Result<impl IntoResponse, (StatusCode, String)> {
    commands::remove_instance_core(&core, &name).map_err(err)?;
    Ok(Json(json!({ "ok": true })))
}

async fn test_instance_handler(
    State(core): State<Arc<AppCore>>,
    Path(name): Path<String>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let core2 = core.clone();
    let result = tokio::task::spawn_blocking(move || commands::test_instance_core(&core2, &name))
        .await
        .map_err(|e| err(format!("测试实例任务失败: {}", e)))?
        .map_err(err)?;
    Ok(to_json(result))
}

#[derive(Deserialize)]
struct BatchPayload {
    #[serde(default)]
    names: Vec<String>,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct BatchAddPayload {
    #[serde(default)]
    nodes: Vec<commands::BatchAddItem>,
    base_port: Option<u16>,
    #[serde(default)]
    use_node_name: Option<bool>,
    name_prefix: Option<String>,
}

async fn batch_add_handler(
    State(core): State<Arc<AppCore>>,
    Json(payload): Json<BatchAddPayload>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let core2 = core.clone();
    let result = tokio::task::spawn_blocking(move || {
        commands::batch_add_core(
            &core2,
            payload.nodes,
            payload.base_port,
            payload.use_node_name,
            payload.name_prefix,
        )
    })
    .await
    .map_err(|e| err(format!("批量添加任务失败: {}", e)))?
    .map_err(err)?;
    Ok(to_json(result))
}

async fn batch_start_handler(
    State(core): State<Arc<AppCore>>,
    Json(payload): Json<BatchPayload>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let core2 = core.clone();
    let names = payload.names;
    let result = tokio::task::spawn_blocking(move || commands::batch_start_core(&core2, names))
        .await
        .map_err(|e| err(format!("批量启动任务失败: {}", e)))?
        .map_err(err)?;
    Ok(to_json(result))
}

async fn batch_stop_handler(
    State(core): State<Arc<AppCore>>,
    Json(payload): Json<BatchPayload>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let core2 = core.clone();
    let names = payload.names;
    let result = tokio::task::spawn_blocking(move || commands::batch_stop_core(&core2, names))
        .await
        .map_err(|e| err(format!("批量停止任务失败: {}", e)))?
        .map_err(err)?;
    Ok(to_json(result))
}

async fn batch_delete_handler(
    State(core): State<Arc<AppCore>>,
    Json(payload): Json<BatchPayload>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let core2 = core.clone();
    let names = payload.names;
    let result = tokio::task::spawn_blocking(move || commands::batch_delete_core(&core2, names))
        .await
        .map_err(|e| err(format!("批量删除任务失败: {}", e)))?
        .map_err(err)?;
    Ok(to_json(result))
}

async fn restart_pool_handler(
    State(core): State<Arc<AppCore>>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let core2 = core.clone();
    let result = tokio::task::spawn_blocking(move || commands::restart_pool_core(&core2))
        .await
        .map_err(|e| err(format!("重启池任务失败: {}", e)))?
        .map_err(err)?;
    Ok(to_json(result))
}

async fn gateway_status_handler(
    State(core): State<Arc<AppCore>>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let status = blocking(move || commands::gateway_status_core(&core)).await?;
    Ok(to_json(status))
}

async fn gateway_stop_handler(
    State(core): State<Arc<AppCore>>,
) -> Result<impl IntoResponse, (StatusCode, String)> {
    blocking(move || commands::gateway_stop_core(&core)).await?;
    Ok(Json(json!({ "ok": true })))
}

#[derive(Deserialize)]
struct RouteModePayload {
    mode: String,
}

async fn gateway_route_mode_handler(
    State(core): State<Arc<AppCore>>,
    Json(payload): Json<RouteModePayload>,
) -> Result<impl IntoResponse, (StatusCode, String)> {
    blocking(move || commands::gateway_set_route_mode_core(&core, &payload.mode)).await?;
    Ok(Json(json!({ "ok": true })))
}

#[derive(Deserialize)]
struct JoinGatewayPayload {
    name: String,
    join: bool,
}

async fn set_join_gateway_handler(
    State(core): State<Arc<AppCore>>,
    Json(payload): Json<JoinGatewayPayload>,
) -> Result<impl IntoResponse, (StatusCode, String)> {
    blocking(move || commands::set_join_gateway_core(&core, &payload.name, payload.join)).await?;
    Ok(Json(json!({ "ok": true })))
}

async fn config_get_handler() -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let cfg = blocking(commands::config_get_core).await?;
    Ok(to_json(cfg))
}

#[derive(Deserialize)]
struct ConfigValuePayload {
    value: String,
}

async fn config_set_handler(
    State(core): State<Arc<AppCore>>,
    Path(key): Path<String>,
    Json(payload): Json<ConfigValuePayload>,
) -> Result<impl IntoResponse, (StatusCode, String)> {
    blocking(move || commands::config_set_core(&core, &key, &payload.value)).await?;
    Ok(Json(json!({ "ok": true })))
}

async fn stats_handler() -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let stats = blocking(commands::get_stats_core).await?;
    Ok(to_json(stats))
}

#[derive(Deserialize)]
struct CallLogQuery {
    limit: Option<usize>,
}

async fn call_log_handler(Query(query): Query<CallLogQuery>) -> Json<serde_json::Value> {
    let limit = query.limit;
    let value = tokio::task::spawn_blocking(move || commands::get_call_log_core(limit))
        .await
        .unwrap_or_default();
    Json(serde_json::to_value(value).unwrap_or(json!({})))
}

async fn call_log_filtered_handler(Json(filter): Json<crate::call_log::CallLogFilter>) -> Json<serde_json::Value> {
    let value = tokio::task::spawn_blocking(move || commands::call_log_filtered_core(&filter))
        .await
        .unwrap_or_default();
    Json(serde_json::to_value(value).unwrap_or(json!({})))
}

async fn call_log_aggregate_handler() -> Json<serde_json::Value> {
    let value = tokio::task::spawn_blocking(commands::call_log_aggregate_core)
        .await
        .unwrap_or_default();
    Json(serde_json::to_value(value).unwrap_or(json!({})))
}

/// 清空统一网关调用日志（core 复用，桌面 command 与 HTTP 同源）。
async fn clear_call_log_handler() -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    blocking(commands::clear_call_log_core).await?;
    Ok(Json(json!({ "ok": true })))
}

#[derive(Deserialize)]
struct StatsResetPayload {
    clear_deleted: Option<bool>,
}

/// 重置 Token 统计（core 复用 reset_stats_core，桌面 command 与 HTTP 同源）。
async fn stats_reset_handler(
    State(core): State<Arc<AppCore>>,
    Json(payload): Json<StatsResetPayload>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let manager = Arc::clone(&core.manager);
    let clear_deleted = payload.clear_deleted.unwrap_or(true);
    let result = blocking(move || commands::reset_stats_core(manager, clear_deleted)).await?;
    Ok(to_json(result))
}

async fn nodes_handler() -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let nodes = blocking(commands::list_nodes_core).await?;
    Ok(to_json(nodes))
}

#[derive(Deserialize)]
struct NodeDeletePayload {
    name: String,
}

async fn node_delete_handler(
    Json(payload): Json<NodeDeletePayload>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let removed = blocking(move || commands::delete_node_core(&payload.name)).await?;
    Ok(to_json(json!({ "removed": removed })))
}

#[derive(Deserialize)]
struct NodeNamesPayload {
    names: Vec<String>,
}

async fn node_delete_batch_handler(
    Json(payload): Json<NodeNamesPayload>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let removed = blocking(move || commands::delete_nodes_core(payload.names)).await?;
    Ok(to_json(json!({ "removed": removed })))
}

async fn binaries_handler() -> Json<serde_json::Value> {
    let value = tokio::task::spawn_blocking(commands::get_binaries_info_core)
        .await
        .map_err(|_| ())
        .and_then(|v| serde_json::to_value(v).map_err(|_| ()))
        .unwrap_or(json!({}));
    Json(value)
}

async fn port_suggest_handler(
    State(core): State<Arc<AppCore>>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let core2 = core.clone();
    let port = tokio::task::spawn_blocking(move || commands::port_suggest_core(&core2))
        .await
        .map_err(|e| err(format!("端口建议任务失败: {}", e)))?
        .map_err(err)?;
    Ok(Json(json!({ "port": port })))
}

#[derive(Deserialize)]
struct PortCheckQuery {
    port: u16,
}

async fn port_check_handler(
    State(core): State<Arc<AppCore>>,
    Query(query): Query<PortCheckQuery>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let core2 = core.clone();
    let port = query.port;
    let result = tokio::task::spawn_blocking(move || commands::port_check_core(&core2, port))
        .await
        .map_err(|e| err(format!("端口检查任务失败: {}", e)))?
        .map_err(err)?;
    Ok(to_json(result))
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct ScanStartPayload {
    nodes: Option<Vec<String>>,
    api_port: Option<u16>,
    socks_port: Option<u16>,
    timeout: Option<u64>,
    concurrency: Option<usize>,
}

async fn scan_start_handler(
    State(core): State<Arc<AppCore>>,
    Json(payload): Json<ScanStartPayload>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let progress = blocking(move || {
        commands::scan_start_core(
            &core,
            commands::ScanStartOpts {
                nodes: payload.nodes,
                api_port: payload.api_port,
                socks_port: payload.socks_port,
                timeout: payload.timeout,
                concurrency: payload.concurrency,
            },
        )
    })
    .await?;
    Ok(to_json(progress))
}

async fn scan_status_handler(
    State(core): State<Arc<AppCore>>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let status = blocking(move || commands::scan_status_core(&core)).await?;
    Ok(to_json(status))
}

async fn scan_stop_handler(
    State(core): State<Arc<AppCore>>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let status = blocking(move || commands::scan_stop_core(&core)).await?;
    Ok(to_json(status))
}

#[derive(Deserialize)]
struct SubscribePayload {
    url: String,
    #[serde(default)]
    join_gateway: bool,
}

async fn subscribe_preview_handler(
    Json(payload): Json<SubscribePayload>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let url = payload.url;
    let result = tokio::task::spawn_blocking(move || commands::subscribe_preview_core(&url))
        .await
        .map_err(|e| err(format!("订阅预览任务失败: {}", e)))?
        .map_err(err)?;
    Ok(to_json(result))
}

async fn subscribe_import_handler(
    State(core): State<Arc<AppCore>>,
    Json(payload): Json<SubscribePayload>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let core2 = core.clone();
    let url = payload.url;
    let join_gateway = payload.join_gateway;
    let result = tokio::task::spawn_blocking(move || {
        commands::subscribe_import_core(&core2, &url, join_gateway)
    })
        .await
        .map_err(|e| err(format!("订阅导入任务失败: {}", e)))?
        .map_err(err)?;
    Ok(to_json(json!({ "imported": result })))
}

async fn subscribe_import_pool_handler(
    Json(payload): Json<SubscribePayload>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let url = payload.url;
    let result = tokio::task::spawn_blocking(move || commands::subscribe_import_pool_core(&url))
        .await
        .map_err(|e| err(format!("订阅导入任务失败: {}", e)))?
        .map_err(err)?;
    Ok(to_json(json!({ "imported": result })))
}

async fn health_check_handler(
    State(core): State<Arc<AppCore>>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let summary = blocking(move || Ok(commands::health_check_now_core(&core))).await?;
    Ok(to_json(summary))
}

async fn health_summary_handler(
    State(core): State<Arc<AppCore>>,
) -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let summary = blocking(move || Ok(commands::health_summary_core(&core))).await?;
    Ok(to_json(summary))
}

async fn autostart_get_handler() -> Result<Json<serde_json::Value>, (StatusCode, String)> {
    let enabled = blocking(commands::autostart_get_core).await?;
    Ok(to_json(json!({ "enabled": enabled })))
}

#[derive(Deserialize)]
struct AutostartPayload {
    enabled: bool,
}

async fn autostart_set_handler(Json(payload): Json<AutostartPayload>) -> Result<impl IntoResponse, (StatusCode, String)> {
    blocking(move || commands::autostart_set_core(payload.enabled)).await?;
    Ok(Json(json!({ "ok": true })))
}

#[derive(Deserialize)]
struct DataCleanPayload {
    level: u8,
}

async fn data_clean_handler(
    State(core): State<Arc<AppCore>>,
    Json(payload): Json<DataCleanPayload>,
) -> Result<impl IntoResponse, (StatusCode, String)> {
    blocking(move || commands::data_clean_core(&core, payload.level)).await?;
    Ok(Json(json!({ "ok": true })))
}

#[derive(Deserialize)]
struct ExportLimitQuery {
    limit: Option<usize>,
}

fn export_text_response(
    body: String,
    content_type: &'static str,
    filename: &'static str,
) -> impl IntoResponse {
    (
        StatusCode::OK,
        [
            (
                axum::http::header::CONTENT_TYPE,
                axum::http::HeaderValue::from_static(content_type),
            ),
            (
                axum::http::header::CONTENT_DISPOSITION,
                axum::http::HeaderValue::from_str(&format!("attachment; filename=\"{}\"", filename))
                    .unwrap_or_else(|_| axum::http::HeaderValue::from_static("attachment")),
            ),
        ],
        body,
    )
}

async fn export_csv_handler(
    Query(query): Query<ExportLimitQuery>,
) -> Result<impl IntoResponse, (StatusCode, String)> {
    let csv = blocking(move || commands::export_call_log_csv_core(query.limit)).await?;
    Ok(export_text_response(csv, "text/csv; charset=utf-8", "call-log.csv"))
}

async fn export_instances_handler(
    State(core): State<Arc<AppCore>>,
) -> Result<impl IntoResponse, (StatusCode, String)> {
    let json = blocking(move || commands::export_instances_json_core(&core)).await?;
    Ok(export_text_response(
        json,
        "application/json; charset=utf-8",
        "instances.json",
    ))
}

async fn export_stats_handler() -> Result<impl IntoResponse, (StatusCode, String)> {
    let json = blocking(commands::export_stats_json_core).await?;
    Ok(export_text_response(
        json,
        "application/json; charset=utf-8",
        "stats.json",
    ))
}
