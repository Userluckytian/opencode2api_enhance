use anyhow::{Context, Result};
use serde::Deserialize;
use std::fs;
use std::path::{Path, PathBuf};

#[derive(Debug, Clone, Default, Deserialize)]
pub struct ClashNode {
    pub name: String,
    pub server: String,
    pub port: u16,
    #[serde(rename = "type")]
    pub node_type: String,
    pub password: Option<String>,
    pub uuid: Option<String>,
    pub cipher: Option<String>,
    pub sni: Option<String>,
    pub servername: Option<String>,
    pub tls: Option<bool>,
    #[serde(rename = "skip-cert-verify")]
    pub skip_cert_verify: Option<bool>,
    pub network: Option<String>,
    /// hysteria2 带宽（Clash 配置里的 up / down）
    pub up: Option<u64>,
    pub down: Option<u64>,
    /// hysteria2 obfs 混淆参数
    pub obfs: Option<String>,
    #[serde(rename = "obfs-password")]
    pub obfs_password: Option<String>,
    #[serde(rename = "ws-opts")]
    pub ws_opts: Option<WsOpts>,
    #[serde(rename = "ws-headers")]
    #[allow(dead_code)]
    pub ws_headers: Option<serde_yaml::Value>,
    #[serde(rename = "client-fingerprint")]
    pub client_fingerprint: Option<String>,
    /// VLESS 流控，例如 xtls-rprx-vision；REALITY 节点必需。
    pub flow: Option<String>,
    /// REALITY 参数（public-key / short-id），缺失会导致 TLS 握手失败。
    #[serde(rename = "reality-opts")]
    pub reality_opts: Option<RealityOpts>,
    #[allow(dead_code)]
    pub alpn: Option<Vec<String>>,
    #[serde(skip)]
    pub group: String,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct RealityOpts {
    #[serde(rename = "public-key")]
    pub public_key: Option<String>,
    /// short-id 在 YAML 里可能未加引号而被解析成数字，这里做宽松转换。
    #[serde(rename = "short-id", default, deserialize_with = "de_loose_string")]
    pub short_id: Option<String>,
}

/// 把标量（字符串/数字/布尔）统一读成 Option<String>。
fn de_loose_string<'de, D>(deserializer: D) -> std::result::Result<Option<String>, D::Error>
where
    D: serde::Deserializer<'de>,
{
    let value = serde_yaml::Value::deserialize(deserializer)?;
    Ok(match value {
        serde_yaml::Value::String(s) => Some(s),
        serde_yaml::Value::Number(n) => Some(n.to_string()),
        serde_yaml::Value::Bool(b) => Some(b.to_string()),
        _ => None,
    })
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct WsOpts {
    pub path: Option<String>,
    pub headers: Option<serde_yaml::Value>,
}

#[derive(Debug, Clone, Default, Deserialize)]
struct ClashProfile {
    proxies: Option<Vec<ClashNode>>,
}

/// 找到 Clash Verge 的 profiles 目录
pub fn find_clash_profiles_dir() -> Option<PathBuf> {
    // Windows: %APPDATA%\io.github.clash-verge-rev.clash-verge-rev\profiles
    #[cfg(windows)]
    {
        if let Ok(appdata) = std::env::var("APPDATA") {
            let dir = PathBuf::from(appdata)
                .join("io.github.clash-verge-rev.clash-verge-rev")
                .join("profiles");
            if dir.exists() {
                return Some(dir);
            }
        }
    }
    // Linux: ~/.local/share/io.github.clash-verge-rev.clash-verge-rev/profiles
    //（Clash Verge Rev 的 Linux 数据目录，遵循 XDG data dir 约定）
    #[cfg(not(windows))]
    {
        if let Some(base) = std::env::var_os("XDG_DATA_HOME")
            .map(PathBuf::from)
            .or_else(|| dirs::home_dir().map(|h| h.join(".local/share")))
        {
            let dir = base
                .join("io.github.clash-verge-rev.clash-verge-rev")
                .join("profiles");
            if dir.exists() {
                return Some(dir);
            }
        }
    }
    None
}

/// 解析单个 Clash YAML 文件中的节点
pub fn parse_clash_yaml(content: &str) -> Result<Vec<ClashNode>> {
    let profile: ClashProfile = serde_yaml::from_str(content)
        .context("Failed to parse Clash YAML")?;
    Ok(profile.proxies.unwrap_or_default())
}

/// 读取单个 Clash YAML 文件中的节点
pub fn parse_clash_yaml_file(path: &Path) -> Result<Vec<ClashNode>> {
    let content = fs::read_to_string(path)
        .with_context(|| format!("Failed to read {}", path.display()))?;
    parse_clash_yaml(&content)
}

/// 扫描 Clash Verge profiles 目录，合并所有节点
pub fn list_local_nodes() -> Result<Vec<ClashNode>> {
    let mut nodes = Vec::new();
    let mut seen = std::collections::HashSet::new();

    if let Some(dir) = find_clash_profiles_dir()
        && let Ok(entries) = fs::read_dir(&dir) {
            for entry in entries.flatten() {
                let path = entry.path();
                if path.extension().and_then(|e| e.to_str()) != Some("yaml") {
                    continue;
                }
                if let Ok(parsed) = parse_clash_yaml_file(&path) {
                    for node in parsed {
                        if seen.insert(node.name.clone()) {
                            nodes.push(node);
                        }
                    }
                }
            }
        }
    nodes.sort_by(|a, b| a.name.cmp(&b.name));
    Ok(nodes)
}

/// 解析 http://host:port/path，返回 (host, port, path)
fn parse_http_url(url: &str) -> Result<(String, u16, String)> {
    if url.starts_with("https://") {
        anyhow::bail!("外部控制暂不支持 https，请使用 http 地址");
    }
    let rest = url
        .strip_prefix("http://")
        .ok_or_else(|| anyhow::anyhow!("地址需以 http:// 开头"))?;
    let (hostport, path) = match rest.split_once('/') {
        Some((h, p)) => (h, format!("/{}", p)),
        None => (rest, "/".to_string()),
    };
    let (host, port) = match hostport.rsplit_once(':') {
        Some((h, p)) => (h.to_string(), p.parse::<u16>().context("端口无效")?),
        None => (hostport.to_string(), 80),
    };
    Ok((host, port, path))
}

/// 解码 HTTP chunked 传输编码；非 chunked 内容原样返回
fn decode_chunked(body: &str) -> String {
    let mut out = String::new();
    let mut rest = body;
    loop {
        let line_end = rest.find("\r\n").unwrap_or(rest.len());
        let size = match usize::from_str_radix(rest[..line_end].trim(), 16) {
            Ok(s) => s,
            Err(_) => {
                if out.is_empty() {
                    return body.to_string();
                }
                break;
            }
        };
        if size == 0 {
            break;
        }
        let start = line_end + 2;
        if start + size > rest.len() {
            break;
        }
        out.push_str(&rest[start..start + size]);
        rest = &rest[start + size..];
        let nl = rest.find("\r\n").unwrap_or(rest.len());
        rest = &rest[(nl + 2).min(rest.len())..];
    }
    out
}

/// 纯 std 实现的最小 HTTP GET（外部控制为本地 http 服务，无需 https/重定向）
fn http_get(url: &str, token: &str) -> Result<String> {
    let (host, port, path) = parse_http_url(url)?;
    let mut stream = std::net::TcpStream::connect((host.as_str(), port))
        .with_context(|| format!("连接 {}:{} 失败", host, port))?;
    stream.set_read_timeout(Some(std::time::Duration::from_secs(8)))?;
    stream.set_write_timeout(Some(std::time::Duration::from_secs(8)))?;
    let auth = if token.is_empty() {
        String::new()
    } else {
        format!("Authorization: Bearer {}\r\n", token)
    };
    let req = format!(
        "GET {} HTTP/1.1\r\nHost: {}:{}\r\nConnection: close\r\n{}\r\n",
        path, host, port, auth
    );
    use std::io::{Read, Write};
    stream.write_all(req.as_bytes()).context("发送请求失败")?;
    let mut buf = Vec::new();
    stream.read_to_end(&mut buf).context("读取响应失败")?;
    let text = String::from_utf8_lossy(&buf);
    let (head, body) = text
        .split_once("\r\n\r\n")
        .unwrap_or((text.as_ref(), ""));
    if !head.starts_with("HTTP/1.1 200") && !head.starts_with("HTTP/1.0 200") {
        anyhow::bail!("外部控制返回非 200：{}", head.lines().next().unwrap_or(""));
    }
    if head.to_lowercase().contains("transfer-encoding: chunked") {
        Ok(decode_chunked(body))
    } else {
        Ok(body.to_string())
    }
}

/// 通过 Clash 外部控制 API（/configs）拉取节点，需 Bearer 授权
fn fetch_nodes_from_api(url: &str, token: &str) -> Result<Vec<ClashNode>> {
    let base = url.trim_end_matches('/').to_string();
    let text = http_get(&format!("{}/configs", base), token)?;
    let value: serde_json::Value =
        serde_json::from_str(&text).context("解析外部控制响应失败")?;
    let proxies = value
        .get("proxies")
        .cloned()
        .ok_or_else(|| anyhow::anyhow!("配置中未找到 proxies"))?;
    let nodes: Vec<ClashNode> =
        serde_json::from_value(proxies).context("解析 proxies 失败")?;
    Ok(nodes)
}

/// 根据节点名推导分组：取「 | 」前段，无则归入「其他」
fn group_from_name(name: &str) -> String {
    let prefix = name.split('|').next().map(|s| s.trim()).unwrap_or("");
    if prefix.is_empty() {
        "其他".to_string()
    } else {
        prefix.to_string()
    }
}

/// 账号信息/官网/邮箱等无法作为节点使用的垃圾节点
fn is_junk_node(name: &str) -> bool {
    if name.starts_with("-----") {
        return true;
    }
    const MARKERS: [&str; 9] = [
        "登录账号", "邮箱:", "官网:", "电报:", "消息:", "体验套餐:", "时间:", "流量重置:", "剩余流量:",
    ];
    let norm = name.replace('：', ":");
    MARKERS.iter().any(|m| norm.contains(m))
}

/// 读取 Clash Verge profiles.yaml，返回 订阅文件名(stem) -> 订阅名 映射
fn profile_name_map() -> std::collections::HashMap<String, String> {
    let mut map = std::collections::HashMap::new();
    let base = find_clash_profiles_dir().and_then(|d| d.parent().map(|p| p.to_path_buf()));
    let path = match base {
        Some(b) => b.join("profiles.yaml"),
        None => return map,
    };
    let content = match fs::read_to_string(&path) {
        Ok(c) => c,
        Err(_) => return map,
    };
    let value: serde_yaml::Value = match serde_yaml::from_str(&content) {
        Ok(v) => v,
        Err(_) => return map,
    };
    if let Some(items) = value.get("items").and_then(|i| i.as_sequence()) {
        for item in items {
            let uid = item.get("uid").and_then(|u| u.as_str()).unwrap_or("");
            let name = item.get("name").and_then(|n| n.as_str()).unwrap_or("");
            if !uid.is_empty() && !name.is_empty() {
                let clean = name.trim().trim_end_matches(".yaml").trim_end_matches(".js");
                map.insert(uid.to_string(), clean.to_string());
            }
        }
    }
    map
}

/// 合并节点来源（外部控制 API 优先 + 本地 profiles 补充），带分组，过滤垃圾节点
pub fn list_nodes_with_group() -> Result<Vec<ClashNode>> {
    let mut nodes = Vec::new();
    let mut seen = std::collections::HashSet::new();

    let cfg = crate::config::Config::load().unwrap_or_default();
    let url = cfg.clash_external_url.trim().to_string();
    let token = cfg.clash_auth_token.trim().to_string();

    if !url.is_empty() {
        if let Ok(list) = fetch_nodes_from_api(&url, &token) {
            for mut node in list {
                if is_junk_node(&node.name) {
                    continue;
                }
                node.group = group_from_name(&node.name);
                if seen.insert(node.name.clone()) {
                    nodes.push(node);
                }
            }
        }
    }

    let name_map = profile_name_map();
    if let Some(dir) = find_clash_profiles_dir()
        && let Ok(entries) = fs::read_dir(&dir) {
            for entry in entries.flatten() {
                let path = entry.path();
                if path.extension().and_then(|e| e.to_str()) != Some("yaml") {
                    continue;
                }
                let stem = path
                    .file_stem()
                    .and_then(|s| s.to_str())
                    .unwrap_or("其他")
                    .to_string();
                let group = name_map
                    .get(&stem)
                    .cloned()
                    .filter(|n| !n.is_empty())
                    .unwrap_or_else(|| stem.clone());
                if let Ok(parsed) = parse_clash_yaml_file(&path) {
                    for mut node in parsed {
                        if is_junk_node(&node.name) {
                            continue;
                        }
                        node.group = group.clone();
                        if seen.insert(node.name.clone()) {
                            nodes.push(node);
                        }
                    }
                }
            }
        }

    // 订阅缓存节点（subscribe.rs 持久化的 Clash YAML / base64 订阅节点），
    // group 统一标记为「订阅」；实例启动同样经本函数查找节点配置，因此
    // 订阅导入的实例在启动时能正常找到节点生成 sing-box 配置。
    for mut node in crate::subscribe::load_subscription_cache()
        .into_iter()
        .map(|n| crate::subscribe::to_clash_node(&n))
    {
        if is_junk_node(&node.name) {
            continue;
        }
        node.group = "订阅".to_string();
        if seen.insert(node.name.clone()) {
            nodes.push(node);
        }
    }
    nodes.sort_by(|a, b| a.group.cmp(&b.group).then(a.name.cmp(&b.name)));
    Ok(nodes)
}

#[cfg(test)]
mod tests {
    use super::*;

    const SAMPLE_TROJAN: &str = r#"
dns:
  enable: true
proxies:
  - {name: "🇸🇬 新加坡 G1", server: 139.177.187.106, port: 26150, type: trojan, password: JbdAgz2NJF, sni: v.qq.com, skip-cert-verify: true}
  - {name: "🇺🇸 美国 G1", server: 23.142.200.93, port: 48249, type: trojan, password: JbdAgz2NJF, sni: v.qq.com, skip-cert-verify: true}
"#;

    const SAMPLE_VLESS: &str = r#"
proxies:
  - {name: CF移动优选1, server: 91.193.59.158, port: 2096, type: vless, uuid: 7a3bac2b-b3ae-4bf6-845a-31fa95bfde26, tls: true, skip-cert-verify: false, servername: edt0099.cuterzhuzhu.eu.org, client-fingerprint: chrome, network: ws, ws-opts: {path: /, headers: {Host: edt0099.cuterzhuzhu.eu.org}}}
"#;

    #[test]
    fn test_parse_trojan() {
        let nodes = parse_clash_yaml(SAMPLE_TROJAN).unwrap();
        assert_eq!(nodes.len(), 2);
        let sg = &nodes[0];
        assert_eq!(sg.name, "🇸🇬 新加坡 G1");
        assert_eq!(sg.server, "139.177.187.106");
        assert_eq!(sg.port, 26150);
        assert_eq!(sg.node_type, "trojan");
        assert_eq!(sg.password.as_deref(), Some("JbdAgz2NJF"));
        assert_eq!(sg.sni.as_deref(), Some("v.qq.com"));
        assert_eq!(sg.skip_cert_verify, Some(true));
    }

    #[test]
    fn test_parse_vless() {
        let nodes = parse_clash_yaml(SAMPLE_VLESS).unwrap();
        assert_eq!(nodes.len(), 1);
        let n = &nodes[0];
        assert_eq!(n.node_type, "vless");
        assert_eq!(n.uuid.as_deref(), Some("7a3bac2b-b3ae-4bf6-845a-31fa95bfde26"));
        assert_eq!(n.network.as_deref(), Some("ws"));
        assert!(n.ws_opts.is_some());
        let ws = n.ws_opts.as_ref().unwrap();
        assert_eq!(ws.path.as_deref(), Some("/"));
        assert!(ws.headers.is_some());
    }

    #[test]
    fn test_parse_empty() {
        let nodes = parse_clash_yaml("dns:\n  enable: true\n").unwrap();
        assert!(nodes.is_empty());
    }

    #[test]
    fn test_parse_http_url() {
        let (h, p, path) = parse_http_url("http://127.0.0.1:9097/configs").unwrap();
        assert_eq!(h, "127.0.0.1");
        assert_eq!(p, 9097);
        assert_eq!(path, "/configs");
        let (h2, p2, path2) = parse_http_url("http://192.168.1.2").unwrap();
        assert_eq!(h2, "192.168.1.2");
        assert_eq!(p2, 80);
        assert_eq!(path2, "/");
        assert!(parse_http_url("https://example.com").is_err());
        assert!(parse_http_url("ftp://x").is_err());
    }

    #[test]
    fn test_decode_chunked() {
        let body = "5\r\nhello\r\n6\r\n world\r\n0\r\n\r\n";
        assert_eq!(decode_chunked(body), "hello world");
        assert_eq!(decode_chunked("plain"), "plain");
    }

    #[test]
    fn test_is_junk_node() {
        assert!(is_junk_node("----- 联系我们 -----"));
        assert!(is_junk_node("登录账号: pzijhsy@f.lm"));
        assert!(is_junk_node("官网: lem688.org/ol"));
        assert!(is_junk_node("邮箱：kefu@falemon.com"));
        assert!(is_junk_node("流量重置: 每月2日 10G"));
        assert!(is_junk_node("🏳️‍🌈 时间: 2026-08-02 09:11:49"));
        assert!(is_junk_node("🕐 流量重置: 每月2日 10G，剩余10G"));
        assert!(!is_junk_node("🇸🇬 新加坡 G1 | 直连、移动优化 | 3x"));
        assert!(!is_junk_node("CF移动优选1"));
        assert!(!is_junk_node("🇯🇵 免费-日本1-Ver.7"));
    }

    #[test]
    fn test_profile_name_map_parses() {
        let yaml = r#"
current: abc
items:
- uid: Merge
  type: merge
  name: null
  file: Merge.yaml
- uid: L8D4f29yTd6a
  type: remote
  name: null
  file: L8D4f29yTd6a.yaml
- uid: LVf8pLQTZFup
  type: local
  name: iKuuu_V2.yaml
  file: LVf8pLQTZFup.yaml
"#;
        let value: serde_yaml::Value = serde_yaml::from_str(yaml).unwrap();
        let mut map = std::collections::HashMap::new();
        if let Some(items) = value.get("items").and_then(|i| i.as_sequence()) {
            for item in items {
                let uid = item.get("uid").and_then(|u| u.as_str()).unwrap_or("");
                let name = item.get("name").and_then(|n| n.as_str()).unwrap_or("");
                if !uid.is_empty() && !name.is_empty() {
                    let clean = name.trim().trim_end_matches(".yaml").trim_end_matches(".js");
                    map.insert(uid.to_string(), clean.to_string());
                }
            }
        }
        assert_eq!(map.get("LVf8pLQTZFup").map(|s| s.as_str()), Some("iKuuu_V2"));
        assert!(!map.contains_key("L8D4f29yTd6a"));
        assert!(!map.contains_key("Merge"));
    }
}
