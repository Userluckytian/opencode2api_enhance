use crate::clash_yaml::ClashNode;
use anyhow::{bail, Context, Result};
use serde_json::json;

/// 解析 Clash hysteria2 带宽字段为 Mbps 数值：支持纯数字（100）、
/// 带单位字符串（"100 Mbps"）、浮点（"1.5 Gbps" → 1500）。
fn parse_bandwidth_mbps(s: &str) -> Option<u64> {
    let t = s.trim();
    if t.is_empty() {
        return None;
    }
    // 提取数字部分与单位
    let num: String = t.chars().take_while(|c| c.is_ascii_digit() || *c == '.').collect();
    let unit = t[num.len()..].to_ascii_lowercase();
    let value: f64 = num.parse().ok()?;
    let mbps = if unit.contains("gbps") || unit.contains("gbit") {
        value * 1000.0
    } else if unit.contains("mbps") || unit.contains("mbit") {
        value
    } else if unit.contains("kbps") || unit.contains("kbit") {
        value / 1000.0
    } else if unit.contains("mb") || unit.contains("m") {
        value
    } else if unit.contains("gb") || unit.contains("g") {
        value * 1000.0
    } else {
        value
    };
    Some(mbps as u64)
}

/// 从 Clash 节点生成 sing-box outbound 配置
fn build_outbound(node: &ClashNode) -> Result<serde_json::Value> {
    match node.node_type.as_str() {
        "trojan" => {
            let password = node.password.as_deref().unwrap_or_default();
            if password.is_empty() {
                bail!("节点 '{}' 缺少 password", node.name);
            }
            Ok(json!({
                "type": "trojan",
                "tag": "proxy",
                "server": node.server,
                "server_port": node.port,
                "password": password,
                "tls": {
                    "enabled": node.tls.unwrap_or(true),
                    "server_name": node.sni.as_deref().or(node.servername.as_deref()).unwrap_or(&node.server),
                    "insecure": node.skip_cert_verify.unwrap_or(false)
                }
            }))
        }
        "vless" => {
            let uuid = node.uuid.as_deref().unwrap_or_default();
            if uuid.is_empty() {
                bail!("节点 '{}' 缺少 uuid", node.name);
            }
            let server_name = node
                .servername
                .as_deref()
                .or(node.sni.as_deref())
                .unwrap_or(&node.server)
                .to_string();

            // REALITY 参数：缺失时服务端会拒绝握手，表现为“节点不可用”。
            let reality = node.reality_opts.as_ref().and_then(|opts| {
                let public_key = opts.public_key.as_deref().unwrap_or_default();
                if public_key.is_empty() {
                    return None;
                }
                let mut value = json!({
                    "enabled": true,
                    "public_key": public_key
                });
                if let Some(short_id) = opts.short_id.as_deref().filter(|s| !s.is_empty()) {
                    value["short_id"] = json!(short_id);
                }
                Some(value)
            });

            let mut tls = json!({
                "enabled": node.tls.unwrap_or(true),
                "server_name": server_name
            });
            match reality {
                // REALITY 自带证书校验，不能与 insecure 混用。
                Some(value) => tls["reality"] = value,
                None => tls["insecure"] = json!(node.skip_cert_verify.unwrap_or(false)),
            }
            // sing-box 要求 REALITY 必须开启 uTLS，缺省指纹用 chrome。
            match node.client_fingerprint.as_deref() {
                Some(fp) if !fp.is_empty() => {
                    tls["utls"] = json!({"enabled": true, "fingerprint": fp});
                }
                _ if tls.get("reality").is_some() => {
                    tls["utls"] = json!({"enabled": true, "fingerprint": "chrome"});
                }
                _ => {}
            }

            let mut outbound = json!({
                "type": "vless",
                "tag": "proxy",
                "server": node.server,
                "server_port": node.port,
                "uuid": uuid,
                "tls": tls
            });
            // xtls-rprx-vision 等流控必须透传，否则 VLESS 握手失败。
            if let Some(flow) = node.flow.as_deref().filter(|f| !f.is_empty()) {
                outbound["flow"] = json!(flow);
            }
            if let Some(transport) = build_transport(node) {
                outbound["transport"] = transport;
            }
            Ok(outbound)
        }
        "vmess" => {
            let uuid = node.uuid.as_deref().unwrap_or_default();
            if uuid.is_empty() {
                bail!("节点 '{}' 缺少 uuid", node.name);
            }
            let mut outbound = json!({
                "type": "vmess",
                "tag": "proxy",
                "server": node.server,
                "server_port": node.port,
                "uuid": uuid,
                "security": "auto",
                "alter_id": 0,
                "tls": {
                    "enabled": node.tls.unwrap_or(false),
                    "server_name": node.servername.as_deref().or(node.sni.as_deref()).unwrap_or(&node.server),
                    "insecure": node.skip_cert_verify.unwrap_or(false)
                },
            });
            if let Some(transport) = build_transport(node) {
                outbound["transport"] = transport;
            }
            Ok(outbound)
        }
        "ss" | "shadowsocks" => {
            let password = node.password.as_deref().unwrap_or_default();
            if password.is_empty() {
                bail!("节点 '{}' 缺少 password", node.name);
            }
            let method = node.cipher.as_deref().unwrap_or("aes-256-gcm");
            Ok(json!({
                "type": "shadowsocks",
                "tag": "proxy",
                "server": node.server,
                "server_port": node.port,
                "method": method,
                "password": password
            }))
        }
        "hysteria2" | "hy2" => {
            let password = node.password.as_deref().unwrap_or_default();
            if password.is_empty() {
                bail!("节点 '{}' 缺少 password", node.name);
            }
            let mut outbound = json!({
                "type": "hysteria2",
                "tag": "proxy",
                "server": node.server,
                "server_port": node.port,
                "password": password,
                "tls": {
                    "enabled": node.tls.unwrap_or(true),
                    "server_name": node
                        .servername
                        .as_deref()
                        .or(node.sni.as_deref())
                        .filter(|s| !s.is_empty())
                        .unwrap_or(&node.server),
                    "insecure": node.skip_cert_verify.unwrap_or(false)
                }
            });
            // Clash 的 obfs: salamander 对应 sing-box 的 salamander 混淆。
            if let Some(obfs) = node.obfs.as_deref().filter(|s| !s.is_empty()) {
                let mut obfs_value = json!({ "type": obfs });
                if let Some(p) = node.obfs_password.as_deref().filter(|s| !s.is_empty()) {
                    obfs_value["password"] = json!(p);
                }
                outbound["obfs"] = obfs_value;
            }
            if let Some(up) = node.up.as_deref().and_then(parse_bandwidth_mbps) {
                outbound["up_mbps"] = json!(up);
            }
            if let Some(down) = node.down.as_deref().and_then(parse_bandwidth_mbps) {
                outbound["down_mbps"] = json!(down);
            }
            Ok(outbound)
        }
        "anytls" => {
            let password = node.password.as_deref().unwrap_or_default();
            if password.is_empty() {
                bail!("节点 '{}' 缺少 password", node.name);
            }
            Ok(json!({
                "type": "anytls",
                "tag": "proxy",
                "server": node.server,
                "server_port": node.port,
                "password": password,
                "tls": {
                    "enabled": node.tls.unwrap_or(true),
                    "server_name": node
                        .servername
                        .as_deref()
                        .or(node.sni.as_deref())
                        .filter(|s| !s.is_empty())
                        .unwrap_or(&node.server),
                    "insecure": node.skip_cert_verify.unwrap_or(false)
                }
            }))
        }
        other => bail!("暂不支持的节点类型: {}", other),
    }
}

/// 构建可选的 V2Ray 传输层配置。
///
/// sing-box 将原生 TCP 表示为“不设置 transport”；`type: tcp` 不是有效的
/// V2Ray transport，会导致配置解析失败。
fn build_transport(node: &ClashNode) -> Option<serde_json::Value> {
    match node.network.as_deref() {
        Some("ws") => {
            let mut headers = json!({});
            let path = node
                .ws_opts
                .as_ref()
                .and_then(|o| o.path.clone())
                .unwrap_or_else(|| "/".to_string());
            if let Some(h) = node.ws_opts.as_ref().and_then(|o| o.headers.as_ref())
                && let Ok(j) = serde_json::to_value(h) {
                    headers = j;
                }
            Some(json!({
                "type": "ws",
                "path": path,
                "headers": headers
            }))
        }
        Some("http") => Some(json!({
            "type": "http",
            "host": null,
            "path": null
        })),
        _ => None,
    }
}

/// 生成完整 sing-box 配置文件
pub fn build_singbox_config(node: &ClashNode, listen_port: u16) -> Result<String> {
    let outbound = build_outbound(node)?;
    let config = json!({
        "log": {
            "level": "warn",
            "timestamp": true
        },
        "inbounds": [
            {
                "type": "socks",
                "listen": "127.0.0.1",
                "listen_port": listen_port
            }
        ],
        "outbounds": [
            outbound,
            {
                "type": "direct",
                "tag": "direct"
            }
        ],
        "route": {
            "final": "proxy"
        }
    });

    serde_json::to_string_pretty(&config).context("Failed to serialize sing-box config")
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::clash_yaml::parse_clash_yaml;

    #[test]
    fn test_parse_bandwidth_mbps() {
        assert_eq!(parse_bandwidth_mbps("100 Mbps"), Some(100));
        assert_eq!(parse_bandwidth_mbps("100"), Some(100));
        assert_eq!(parse_bandwidth_mbps("1.5 Gbps"), Some(1500));
        assert_eq!(parse_bandwidth_mbps("1000 Kbps"), Some(1));
        assert_eq!(parse_bandwidth_mbps(""), None);
        assert_eq!(parse_bandwidth_mbps("abc"), None);
    }

    #[test]
    fn test_build_trojan_config() {
        let yaml = r#"
proxies:
  - {name: "🇸🇬 新加坡 G1", server: 139.177.187.106, port: 26150, type: trojan, password: JbdAgz2NJF, sni: v.qq.com, skip-cert-verify: true}
"#;
        let nodes = parse_clash_yaml(yaml).unwrap();
        let config = build_singbox_config(&nodes[0], 7890).unwrap();
        let v: serde_json::Value = serde_json::from_str(&config).unwrap();
        let ob = &v["outbounds"][0];
        assert_eq!(ob["type"], "trojan");
        assert_eq!(ob["server"], "139.177.187.106");
        assert_eq!(ob["server_port"], 26150);
        assert_eq!(ob["password"], "JbdAgz2NJF");
        assert_eq!(ob["tls"]["server_name"], "v.qq.com");
        assert_eq!(ob["tls"]["insecure"], true);
        assert_eq!(v["inbounds"][0]["listen_port"], 7890);
        assert_eq!(v["route"]["final"], "proxy");
    }

    #[test]
    fn test_build_vless_config() {
        let yaml = r#"
proxies:
  - {name: CF移动优选1, server: 91.193.59.158, port: 2096, type: vless, uuid: 7a3bac2b-b3ae-4bf6-845a-31fa95bfde26, tls: true, skip-cert-verify: false, servername: edt0099.cuterzhuzhu.eu.org, client-fingerprint: chrome, network: ws, ws-opts: {path: /, headers: {Host: edt0099.cuterzhuzhu.eu.org}}}
"#;
        let nodes = parse_clash_yaml(yaml).unwrap();
        let config = build_singbox_config(&nodes[0], 7891).unwrap();
        let v: serde_json::Value = serde_json::from_str(&config).unwrap();
        let ob = &v["outbounds"][0];
        assert_eq!(ob["type"], "vless");
        assert_eq!(ob["uuid"], "7a3bac2b-b3ae-4bf6-845a-31fa95bfde26");
        assert_eq!(ob["tls"]["server_name"], "edt0099.cuterzhuzhu.eu.org");
        assert_eq!(ob["tls"]["utls"]["fingerprint"], "chrome");
        assert_eq!(ob["transport"]["type"], "ws");
        assert_eq!(ob["transport"]["path"], "/");
    }

    #[test]
    fn test_unsupported_type() {
        let yaml = r#"
proxies:
  - {name: test, server: 1.2.3.4, port: 80, type: tuic}
"#;
        let nodes = parse_clash_yaml(yaml).unwrap();
        let result = build_singbox_config(&nodes[0], 7892);
        assert!(result.is_err());
    }

    #[test]
    fn test_vless_reality_vision() {
        let yaml = r#"
proxies:
  - { name: reality-vision, type: vless, server: 1.2.3.4, port: 8443, uuid: 487bb953-b9f5-4fde-ad88-a478d57d4318, network: tcp, udp: true, flow: xtls-rprx-vision, tls: true, skip-cert-verify: true, servername: www.apple.com, reality-opts: { public-key: zgjBCRuiqIKlbJDDbJalwYy3fagiMt7YL4xeItbvfVE, short-id: 7f3a9c2b }, client-fingerprint: safari }
  - { name: reality-numeric-shortid, type: vless, server: 1.2.3.4, port: 8443, uuid: 487bb953-b9f5-4fde-ad88-a478d57d4318, flow: xtls-rprx-vision, tls: true, servername: s.yimg.jp, reality-opts: { public-key: Af519EdH4WyDNcsNmbUwPYvIgVKr_-ID3x7TPZr23gY, short-id: 890213 } }
"#;
        let nodes = parse_clash_yaml(yaml).unwrap();

        let config = build_singbox_config(&nodes[0], 7895).unwrap();
        let v: serde_json::Value = serde_json::from_str(&config).unwrap();
        let ob = &v["outbounds"][0];
        assert_eq!(ob["flow"], "xtls-rprx-vision");
        assert_eq!(ob["tls"]["reality"]["enabled"], true);
        assert_eq!(
            ob["tls"]["reality"]["public_key"],
            "zgjBCRuiqIKlbJDDbJalwYy3fagiMt7YL4xeItbvfVE"
        );
        assert_eq!(ob["tls"]["reality"]["short_id"], "7f3a9c2b");
        assert_eq!(ob["tls"]["utls"]["fingerprint"], "safari");
        // REALITY 与 insecure 互斥
        assert!(ob["tls"].get("insecure").is_none());
        assert!(ob.get("transport").is_none());

        // 未加引号的数字 short-id 也要能读成字符串，并自动补 uTLS 指纹
        let config = build_singbox_config(&nodes[1], 7896).unwrap();
        let v: serde_json::Value = serde_json::from_str(&config).unwrap();
        let ob = &v["outbounds"][0];
        assert_eq!(ob["tls"]["reality"]["short_id"], "890213");
        assert_eq!(ob["tls"]["utls"]["fingerprint"], "chrome");
    }

    #[test]
    fn test_native_tcp_omits_transport() {
        let yaml = r#"
proxies:
  - {name: vmess-default, server: vmess.example.com, port: 443, type: vmess, uuid: ac996880-7705-352f-8d47-7ccc9e6f4b4c}
  - {name: vless-tcp, server: vless.example.com, port: 443, type: vless, uuid: 7a3bac2b-b3ae-4bf6-845a-31fa95bfde26, network: tcp}
"#;
        let nodes = parse_clash_yaml(yaml).unwrap();

        for (index, port) in [7892, 7893].into_iter().enumerate() {
            let config = build_singbox_config(&nodes[index], port).unwrap();
            let value: serde_json::Value = serde_json::from_str(&config).unwrap();
            assert!(
                value["outbounds"][0].get("transport").is_none(),
                "native TCP transport must be omitted"
            );
        }
    }

    #[test]
    fn test_build_hysteria2_config() {
        let yaml = r#"
proxies:
  - {name: hy2-jp, server: bage3.ravenhash.org, port: 13353, type: hysteria2, password: pass123, sni: jp.example.com, skip-cert-verify: true, up: 200, down: 199, obfs: salamander, obfs-password: abc123}
  - {name: hy2-plain, server: jpali.fuuny.org, port: 25565, type: hysteria2, password: pass456}
"#;
        let nodes = parse_clash_yaml(yaml).unwrap();

        let config = build_singbox_config(&nodes[0], 7897).unwrap();
        let v: serde_json::Value = serde_json::from_str(&config).unwrap();
        let ob = &v["outbounds"][0];
        assert_eq!(ob["type"], "hysteria2");
        assert_eq!(ob["password"], "pass123");
        assert_eq!(ob["tls"]["server_name"], "jp.example.com");
        assert_eq!(ob["tls"]["insecure"], true);
        assert_eq!(ob["up_mbps"], 200);
        assert_eq!(ob["down_mbps"], 199);
        assert_eq!(ob["obfs"]["type"], "salamander");
        assert_eq!(ob["obfs"]["password"], "abc123");

        let config = build_singbox_config(&nodes[1], 7898).unwrap();
        let v: serde_json::Value = serde_json::from_str(&config).unwrap();
        let ob = &v["outbounds"][0];
        assert!(ob.get("obfs").is_none());
        assert!(ob.get("up_mbps").is_none());
        assert_eq!(ob["tls"]["server_name"], "jpali.fuuny.org");
    }

    #[test]
    fn test_build_anytls_config() {
        let yaml = r#"
proxies:
  - {name: anytls-hk, server: hklumen.094180.xyz, port: 9999, type: anytls, password: a5b309a5-d952-4fa2-9630-901ffeb1f429, skip-cert-verify: true}
"#;
        let nodes = parse_clash_yaml(yaml).unwrap();
        let config = build_singbox_config(&nodes[0], 7899).unwrap();
        let v: serde_json::Value = serde_json::from_str(&config).unwrap();
        let ob = &v["outbounds"][0];
        assert_eq!(ob["type"], "anytls");
        assert_eq!(ob["server"], "hklumen.094180.xyz");
        assert_eq!(ob["server_port"], 9999);
        assert_eq!(ob["password"], "a5b309a5-d952-4fa2-9630-901ffeb1f429");
        assert_eq!(ob["tls"]["insecure"], true);
    }
}
