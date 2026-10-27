# Go Coverage for Zed

Marks Go code that tests do not cover. Zed extensions cannot paint line
backgrounds, so uncovered blocks are reported as diagnostics from a small
language server, `go-coverage-lsp`.

## Setup

1. Install the extension from `zed: extensions` (search for "Go Coverage").
   It downloads the `go-coverage-lsp` server from this repository's GitHub
   releases. If `go-coverage-lsp` is already on your `PATH`, that copy is used
   instead.
2. Enable the server for Go in your Zed settings:

   ```json
   {
     "languages": {
       "Go": { "language_servers": ["go-coverage", "..."] }
     }
   }
   ```

3. Generate a profile in the project root: `go test -coverprofile=coverage.out ./...`

To build from source instead, install the server with
`go install github.com/benipranata/zed-go-coverage/server/cmd/go-coverage-lsp@latest`
and install this repository as a dev extension (`zed: install dev extension`).
Building the extension needs Rust installed through rustup.

Open files update when the profile changes and on save. Edits clear the marks
until the next save, because the profile no longer matches the buffer. Marks
shown after a save reflect the last profile, so re-run the tests to refresh
them. If the profile cannot be read or parsed, the server logs the error once.

## Configuration

All options go under `lsp.go-coverage.initialization_options` in your Zed
settings. They are optional.

| Option        | Values                                    | Default         | Effect                                                              |
| ------------- | ----------------------------------------- | --------------- | ------------------------------------------------------------------- |
| `profile`     | path, relative to the project root or absolute | `coverage.out` | Coverage profile to read.                                           |
| `show`        | `"all"`, `"uncovered"`, `"covered"`       | `"all"`         | Which blocks to mark. Unknown values fall back to `"all"`.          |
| `diagnostics` | `true`, `false`                           | `true`          | Also report uncovered blocks as diagnostics (the underline).        |

Zed settings that affect how coverage is drawn:

| Setting                                  | Values                                              | Effect                                                            |
| ---------------------------------------- | --------------------------------------------------- | ----------------------------------------------------------------- |
| `lsp_document_colors`                    | `"background"`, `"border"`, `"inlay"`, `"none"`     | How Zed draws the colors this server reports. Also affects other servers that report colors. |

After changing any option or setting, restart the language server with
`editor: restart language server`. Options are only read at startup. After
changing the Rust extension, run `zed: rebuild dev extension` instead.

### Example: red borders on untested code

```json
{
  "lsp_document_colors": "border",
  "languages": {
    "Go": { "language_servers": ["go-coverage", "..."] }
  },
  "lsp": {
    "go-coverage": {
      "initialization_options": {
        "profile": "coverage.out",
        "show": "uncovered",
        "diagnostics": false
      }
    }
  }
}
```

Use `"lsp_document_colors": "background"` for a fill, and `"show": "all"` to
see covered code in green as well. Colors are fixed: translucent green for
covered and red for uncovered.

Highlights cover the text of each block, not the full editor width.

## Development

```sh
cd server && go test ./... -cover
```

## Releasing

Push a `vX.Y.Z` tag. The release workflow tests the server, builds archives
for macOS, Linux and Windows with `scripts/package.sh`, and publishes them as a
GitHub release. Also tag the Go module as `server/vX.Y.Z` so `go install` can
resolve a version.
