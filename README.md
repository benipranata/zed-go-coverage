# Go Coverage for Zed

Marks Go code that tests do not cover. Zed extensions cannot paint line
backgrounds, so uncovered blocks are reported as diagnostics from a small
language server, `go-coverage-lsp`.

## Setup

1. Install the server: `go install github.com/benipranata/zed-go-coverage/server/cmd/go-coverage-lsp@latest`
2. Install this repository as a dev extension (`zed: install dev extension`). Building it needs Rust.
3. Enable the server for Go in your Zed settings:

   ```json
   {
     "languages": {
       "Go": { "language_servers": ["go-coverage", "..."] }
     }
   }
   ```

4. Generate a profile in the project root: `go test -coverprofile=coverage.out ./...`

Open files update when the profile changes and on save. Edits clear the marks
until the next save, because the profile no longer matches the buffer. Marks
shown after a save reflect the last profile, so re-run the tests to refresh
them. If the profile cannot be read or parsed, the server logs the error once.

## Configuration

The server reads `coverage.out` in the project root by default. If you write the
profile elsewhere, for example with `go test -coverprofile=out/cover.out ./...`,
set its path (relative to the project root or absolute):

```json
{
  "lsp": {
    "go-coverage": { "initialization_options": { "profile": "out/cover.out" } }
  }
}
```

## Development

```sh
cd server && go test ./... -cover
```
