use anyhow::{Context, Result};
use std::fs;
use std::path::Path;

// 内嵌子程序源：按平台由 build.rs 的 cfg 选择（Windows 版 bin/*.exe；
// Linux/macOS 优先 bin/* 无扩展名，缺失时回退 .exe）。platform_name 决定
// 释放时的目标文件名——与 instance.rs/probe.rs 的解析逻辑对应。
#[cfg(embed_unix_bin)]
pub const OPENCODE2API: &[u8] = include_bytes!("../../bin/opencode2api");
#[cfg(embed_unix_bin)]
pub const SINGBOX: &[u8] = include_bytes!("../../bin/sing-box");
#[cfg(not(embed_unix_bin))]
pub const OPENCODE2API: &[u8] = include_bytes!("../../bin/opencode2api.exe");
#[cfg(not(embed_unix_bin))]
pub const SINGBOX: &[u8] = include_bytes!("../../bin/sing-box.exe");

/// 当前平台下子程序的文件名（Windows 带 .exe，其余平台不带）
fn platform_name(name: &str) -> String {
    if cfg!(windows) {
        format!("{}.exe", name)
    } else {
        name.to_string()
    }
}

/// 确保 bin 目录下的两个子程序存在且与内嵌版本一致。
/// 返回是否发生了写入（True 表示首次释放或更新）。
pub fn ensure_binaries(bin_dir: &Path) -> Result<bool> {
    fs::create_dir_all(bin_dir).with_context(|| format!("创建目录失败: {}", bin_dir.display()))?;
    let wrote_oc = ensure_file(bin_dir, &platform_name("opencode2api"), OPENCODE2API)?;
    let wrote_sb = ensure_file(bin_dir, &platform_name("sing-box"), SINGBOX)?;
    Ok(wrote_oc || wrote_sb)
}

fn ensure_file(dir: &Path, name: &str, data: &[u8]) -> Result<bool> {
    let path = dir.join(name);
    let need_write = match fs::metadata(&path) {
        Ok(m) => m.len() != data.len() as u64,
        Err(_) => true,
    };
    if need_write {
        fs::write(&path, data)
            .with_context(|| format!("写入 {} 失败: {}", name, path.display()))?;
        Ok(true)
    } else {
        Ok(false)
    }
}
