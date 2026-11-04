fn main() {
    println!("cargo:rustc-check-cfg=cfg(embed_windows_bin)");
    println!("cargo:rustc-check-cfg=cfg(embed_unix_bin)");
    println!("cargo:rustc-check-cfg=cfg(embed_free_tier_engine)");
    // 按目标平台设置内嵌子程序的源文件名（embed.rs 据此条件编译 include_bytes）。
    // - Windows：bin/opencode2api.exe（CI 自行构建）
    // - Linux/macOS：优先 bin/opencode2api（平台对应二进制）；
    //   缺失时回退 .exe（保证本地交叉/未准备产物时仍可编译）
    let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("..");
    let target_os = std::env::var("CARGO_CFG_TARGET_OS").unwrap_or_default();
    let mut unix_bins = false;
    match target_os.as_str() {
        "windows" => {
            println!("cargo:rustc-cfg=embed_windows_bin");
        }
        "linux" | "macos" => {
            if root.join("bin/opencode2api").exists() {
                unix_bins = true;
                println!("cargo:rustc-cfg=embed_unix_bin");
            } else {
                println!("cargo:rustc-cfg=embed_windows_bin");
            }
        }
        _ => {
            println!("cargo:rustc-cfg=embed_windows_bin");
        }
    }

    // 免费层引擎（40080，opencode2api-win，GPL-3.0 独立进程）：存在才内嵌。
    // 命名规则与 embed.rs 的 cfg 组合保持一致：选到 unix 产物用无扩展名，否则用 .exe。
    // 缺失不阻断编译——面板会在「免费层」状态里提示未安装，并引导执行
    // scripts/fetch-free-tier-engine.ps1 拉取/构建引擎。
    let engine_name = if unix_bins {
        "opencode2api-win"
    } else {
        "opencode2api-win.exe"
    };
    let engine = root.join("bin").join(engine_name);
    // 无条件登记变更：先缺文件构建过一次的工程，后续放入引擎也能触发重编。
    println!("cargo:rerun-if-changed={}", engine.display());
    if engine.exists() {
        println!("cargo:rustc-cfg=embed_free_tier_engine");
    } else {
        println!(
            "cargo:warning=bin/{} not found; free-tier engine (40080) will not be bundled (run scripts/fetch-free-tier-engine.ps1)",
            engine_name
        );
    }

    tauri_build::build()
}
