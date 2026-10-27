use std::fs;

use zed_extension_api::{
    self as zed, Architecture, DownloadedFileType, GithubReleaseOptions, LanguageServerId,
    LanguageServerInstallationStatus, Os, Result,
};

const BINARY: &str = "go-coverage-lsp";
const REPO: &str = "benipranata/zed-go-coverage";

struct GoCoverage {
    cached_binary_path: Option<String>,
}

impl GoCoverage {
    fn binary_path(
        &mut self,
        language_server_id: &LanguageServerId,
        worktree: &zed::Worktree,
    ) -> Result<String> {
        if let Some(path) = worktree.which(BINARY) {
            return Ok(path);
        }
        if let Some(path) = &self.cached_binary_path {
            if fs::metadata(path).is_ok_and(|m| m.is_file()) {
                return Ok(path.clone());
            }
        }

        zed::set_language_server_installation_status(
            language_server_id,
            &LanguageServerInstallationStatus::CheckingForUpdate,
        );
        let path = download(language_server_id).inspect_err(|err| {
            zed::set_language_server_installation_status(
                language_server_id,
                &LanguageServerInstallationStatus::Failed(err.clone()),
            );
        })?;
        zed::set_language_server_installation_status(
            language_server_id,
            &LanguageServerInstallationStatus::None,
        );
        self.cached_binary_path = Some(path.clone());
        Ok(path)
    }
}

fn download(language_server_id: &LanguageServerId) -> Result<String> {
    let release = zed::latest_github_release(
        REPO,
        GithubReleaseOptions {
            require_assets: true,
            pre_release: false,
        },
    )
    .map_err(|err| format!("{err}; or install it with `go install github.com/{REPO}/server/cmd/{BINARY}@latest`"))?;

    let (os, arch) = zed::current_platform();
    let (os_name, ext, file_type, exe) = match os {
        Os::Mac => ("darwin", "tar.gz", DownloadedFileType::GzipTar, ""),
        Os::Linux => ("linux", "tar.gz", DownloadedFileType::GzipTar, ""),
        Os::Windows => ("windows", "zip", DownloadedFileType::Zip, ".exe"),
    };
    let arch_name = match arch {
        Architecture::Aarch64 => "arm64",
        Architecture::X8664 => "amd64",
        Architecture::X86 => return Err("32-bit x86 is not supported".to_string()),
    };

    let asset_name = format!("{BINARY}-{os_name}-{arch_name}.{ext}");
    let asset = release
        .assets
        .iter()
        .find(|asset| asset.name == asset_name)
        .ok_or_else(|| format!("release {} has no asset {asset_name}", release.version))?;

    let dir = format!("{BINARY}-{}", release.version);
    let path = format!("{dir}/{BINARY}{exe}");
    if !fs::metadata(&path).is_ok_and(|m| m.is_file()) {
        zed::set_language_server_installation_status(
            language_server_id,
            &LanguageServerInstallationStatus::Downloading,
        );
        zed::download_file(&asset.download_url, &dir, file_type)
            .map_err(|err| format!("failed to download {asset_name}: {err}"))?;
        zed::make_file_executable(&path)?;
        remove_old_versions(&dir);
    }
    Ok(path)
}

fn remove_old_versions(keep: &str) {
    let Ok(entries) = fs::read_dir(".") else {
        return;
    };
    for entry in entries.flatten() {
        let name = entry.file_name();
        let name = name.to_string_lossy();
        if name.starts_with(&format!("{BINARY}-")) && name != keep {
            let _ = fs::remove_dir_all(entry.path());
        }
    }
}

impl zed::Extension for GoCoverage {
    fn new() -> Self {
        Self {
            cached_binary_path: None,
        }
    }

    fn language_server_command(
        &mut self,
        language_server_id: &LanguageServerId,
        worktree: &zed::Worktree,
    ) -> Result<zed::Command> {
        Ok(zed::Command {
            command: self.binary_path(language_server_id, worktree)?,
            args: vec![],
            env: worktree.shell_env(),
        })
    }
}

zed::register_extension!(GoCoverage);
