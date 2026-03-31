use std::fs;
use std::path::{Path, PathBuf};

use anyhow::{Context, Result, anyhow};

const AUTOSTART_ENTRY_BASENAME: &str = "minipunch-desktop";

#[derive(Debug, Clone)]
pub struct AutostartStatus {
    pub enabled: bool,
    pub entry_path: PathBuf,
    pub platform_label: &'static str,
    pub detail: String,
}

pub fn detect_autostart(config_path: &Path) -> Result<AutostartStatus> {
    let entry_path = autostart_entry_path()?;
    let expected = render_autostart_entry(config_path)?;
    let status = match fs::read_to_string(&entry_path) {
        Ok(existing) if normalize_contents(&existing) == normalize_contents(&expected) => {
            AutostartStatus {
                enabled: true,
                entry_path,
                platform_label: autostart_platform_label(),
                detail: "当前配置对应的自启动入口已安装。".to_string(),
            }
        }
        Ok(_) => AutostartStatus {
            enabled: false,
            entry_path,
            platform_label: autostart_platform_label(),
            detail: "已存在自启动入口，但与当前配置路径或启动模式不一致。".to_string(),
        },
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => AutostartStatus {
            enabled: false,
            entry_path,
            platform_label: autostart_platform_label(),
            detail: "当前尚未安装自启动入口。".to_string(),
        },
        Err(err) => {
            return Err(err)
                .with_context(|| format!("读取自启动入口失败：{}", entry_path.display()));
        }
    };
    Ok(status)
}

pub fn enable_autostart(config_path: &Path) -> Result<AutostartStatus> {
    let entry_path = autostart_entry_path()?;
    let entry = render_autostart_entry(config_path)?;
    let parent = entry_path
        .parent()
        .ok_or_else(|| anyhow!("自启动入口路径没有父目录"))?;
    fs::create_dir_all(parent).with_context(|| format!("创建目录失败：{}", parent.display()))?;
    fs::write(&entry_path, entry)
        .with_context(|| format!("写入自启动入口失败：{}", entry_path.display()))?;
    detect_autostart(config_path)
}

pub fn disable_autostart(config_path: &Path) -> Result<AutostartStatus> {
    let entry_path = autostart_entry_path()?;
    match fs::remove_file(&entry_path) {
        Ok(()) => {}
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => {}
        Err(err) => {
            return Err(err)
                .with_context(|| format!("移除自启动入口失败：{}", entry_path.display()));
        }
    }
    detect_autostart(config_path)
}

fn autostart_entry_path() -> Result<PathBuf> {
    #[cfg(target_os = "macos")]
    {
        let home = dirs::home_dir().ok_or_else(|| anyhow!("无法解析用户主目录"))?;
        return Ok(home
            .join("Library")
            .join("LaunchAgents")
            .join(format!("{AUTOSTART_ENTRY_BASENAME}.plist")));
    }

    #[cfg(target_os = "linux")]
    {
        let config_dir = dirs::config_dir().ok_or_else(|| anyhow!("无法解析配置目录"))?;
        return Ok(config_dir
            .join("autostart")
            .join(format!("{AUTOSTART_ENTRY_BASENAME}.desktop")));
    }

    #[cfg(target_os = "windows")]
    {
        let data_dir = dirs::data_dir().ok_or_else(|| anyhow!("无法解析 AppData 目录"))?;
        return Ok(windows_startup_entry_path(&data_dir));
    }

    #[allow(unreachable_code)]
    Err(anyhow!("当前平台暂不支持自启动"))
}

fn render_autostart_entry(config_path: &Path) -> Result<String> {
    let exe_path = absolute_path(std::env::current_exe()?);
    let config_path = absolute_path(config_path.to_path_buf());

    #[cfg(target_os = "macos")]
    {
        return Ok(format!(
            r#"<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{AUTOSTART_ENTRY_BASENAME}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{}</string>
        <string>--autostart</string>
        <string>--config</string>
        <string>{}</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <false/>
</dict>
</plist>
"#,
            xml_escape(&exe_path.to_string_lossy()),
            xml_escape(&config_path.to_string_lossy()),
        ));
    }

    #[cfg(target_os = "linux")]
    {
        return Ok(format!(
            "[Desktop Entry]\nType=Application\nVersion=1.0\nName=MiniPunch 桌面端\nComment=MiniPunch 极简私网桌面端\nExec={} --autostart --config {}\nTerminal=false\nHidden=false\nX-GNOME-Autostart-enabled=true\n",
            desktop_quote_arg(&exe_path.to_string_lossy()),
            desktop_quote_arg(&config_path.to_string_lossy()),
        ));
    }

    #[cfg(target_os = "windows")]
    {
        return Ok(render_windows_autostart_entry(&exe_path, &config_path));
    }

    #[allow(unreachable_code)]
    Err(anyhow!("当前平台暂不支持自启动"))
}

fn autostart_platform_label() -> &'static str {
    #[cfg(target_os = "macos")]
    {
        return "launchd";
    }
    #[cfg(target_os = "linux")]
    {
        return "xdg-autostart";
    }
    #[cfg(target_os = "windows")]
    {
        return "startup-folder";
    }
    #[allow(unreachable_code)]
    "unsupported"
}

fn absolute_path(path: PathBuf) -> PathBuf {
    path.canonicalize().unwrap_or(path)
}

fn normalize_contents(value: &str) -> String {
    value.replace("\r\n", "\n").trim().to_string()
}

fn xml_escape(value: &str) -> String {
    value
        .replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
        .replace('"', "&quot;")
        .replace('\'', "&apos;")
}

#[cfg(target_os = "linux")]
fn desktop_quote_arg(value: &str) -> String {
    let escaped = value
        .replace('\\', "\\\\")
        .replace('"', "\\\"")
        .replace('`', "\\`")
        .replace('$', "\\$");
    format!("\"{escaped}\"")
}

#[cfg(any(target_os = "windows", test))]
fn windows_startup_entry_path(data_dir: &Path) -> PathBuf {
    data_dir
        .join("Microsoft")
        .join("Windows")
        .join("Start Menu")
        .join("Programs")
        .join("Startup")
        .join(format!("{AUTOSTART_ENTRY_BASENAME}.vbs"))
}

#[cfg(any(target_os = "windows", test))]
fn render_windows_autostart_entry(exe_path: &Path, config_path: &Path) -> String {
    let exe = windows_vbs_escape(&exe_path.to_string_lossy());
    let config = windows_vbs_escape(&config_path.to_string_lossy());
    format!(
        "Set shell = CreateObject(\"WScript.Shell\")\r\nshell.Run \"\"\"\" & \"{exe}\" & \"\"\"\" & \" --autostart --config \" & \"\"\"\" & \"{config}\" & \"\"\"\", 0, False\r\nSet shell = Nothing\r\n"
    )
}

#[cfg(any(target_os = "windows", test))]
fn windows_vbs_escape(value: &str) -> String {
    value.replace('"', "\"\"")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn windows_startup_entry_uses_hidden_vbs_launcher() {
        let entry = render_windows_autostart_entry(
            Path::new(r"C:\MiniPunch\minipunch-desktop.exe"),
            Path::new(r"C:\MiniPunch\agent.toml"),
        );
        assert!(entry.contains("WScript.Shell"));
        assert!(entry.contains("shell.Run"));
        assert!(entry.contains("--autostart --config"));
        assert!(entry.contains(r#"C:\MiniPunch\minipunch-desktop.exe"#));
        assert!(entry.contains(r#"C:\MiniPunch\agent.toml"#));
        assert!(entry.contains(", 0, False"));
    }

    #[test]
    fn windows_startup_entry_path_uses_vbs_extension() {
        let path = windows_startup_entry_path(Path::new(r"C:\Users\demo\AppData\Roaming"));
        let rendered = path.to_string_lossy();
        assert!(rendered.ends_with("minipunch-desktop.vbs"));
        assert!(rendered.contains("Microsoft"));
        assert!(rendered.contains("Startup"));
    }
}
