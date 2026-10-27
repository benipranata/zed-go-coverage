use zed_extension_api::{self as zed, LanguageServerId, Result};

const BINARY: &str = "go-coverage-lsp";

struct GoCoverage;

impl zed::Extension for GoCoverage {
    fn new() -> Self {
        Self
    }

    fn language_server_command(
        &mut self,
        _language_server_id: &LanguageServerId,
        worktree: &zed::Worktree,
    ) -> Result<zed::Command> {
        let command = worktree.which(BINARY).ok_or_else(|| {
            format!("{BINARY} not found in PATH; install it with `go install github.com/benipranata/zed-go-coverage/server/cmd/{BINARY}@latest`")
        })?;
        Ok(zed::Command {
            command,
            args: vec![],
            env: worktree.shell_env(),
        })
    }
}

zed::register_extension!(GoCoverage);
