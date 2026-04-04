# stake-cli

`stake-cli` is a focused Go project for working with [Stake](https://hellostake.com) session-token-backed accounts.

It exposes two binaries:

- `stake-cli`, a command-line client that talks directly to the Stake API using locally stored session tokens
- `stake-api-server`, a local mirror that keeps stored sessions warm, serves account metadata, and proxies requests through those sessions

## Features

- XDG-backed local auth store
- Direct CLI access to Stake using stored session tokens
- Local mirror and read-only REST API for multi-account access
- Background session keepalive to help short-lived tokens stay usable
- Normalized ASX and US trades from Stake

## Library Usage

The direct Stake client is publicly importable for other tools and libraries:

```go
import "github.com/lox/stake-cli/pkg/stake"
```

## Auth Setup

To find a Stake session token:

1. Log in to [Stake](https://hellostake.com) in your browser.
2. Select the account you want to use.
3. Open browser DevTools and go to the Network tab.
4. Filter requests by `Fetch/XHR` and search for `/api/user`.
5. Open one of those requests and copy the `stake-session-token` request header value.

Treat that token like a password.

Add one or more Stake accounts with a local name and session token:

```bash
./dist/stake-cli auth add primary --token "your-session-token"
./dist/stake-cli auth add secondary --token "your-session-token"
```

Stored auth defaults to:

```text
${XDG_CONFIG_HOME:-~/.config}/stake-cli/accounts.json
```

The CLI validates the token against `/api/user`, stores the discovered account metadata, and will persist any fresher `Stake-Session-Token` value that later responses return.

Useful auth commands:

```bash
./dist/stake-cli auth list
./dist/stake-cli auth remove primary
```

## Direct CLI

Once auth is stored locally, `stake-cli` talks directly to Stake:

```bash
./dist/stake-cli user primary
./dist/stake-cli trades primary
```

For testing, you can point the CLI at a different API base URL:

```bash
./dist/stake-cli --base-url http://127.0.0.1:18081 user primary
```

## Server

`stake-api-server` reads the same auth store, periodically validates each stored token, and persists any refreshed token value it sees in response headers.

```bash
mise trust
mise install
mise run build

./dist/stake-api-server --account primary
```

Or run directly:

```bash
mise run run -- --account primary
```

Useful server flags:

- `--auth-store ~/.config/stake-cli/accounts.json`
- `--account primary --account secondary`
- `--listen 127.0.0.1:8081`
- `--refresh-interval 15m`
- `--shutdown-timeout 10s`

## API

- `GET /healthz`
- `GET /v1/accounts`
- `GET /v1/accounts/{account}`
- `GET /v1/accounts/{account}/user`
- `GET /v1/accounts/{account}/trades`
- `ANY /v1/accounts/{account}/mirror/{path...}`

Example:

```bash
curl http://127.0.0.1:8081/v1/accounts
curl http://127.0.0.1:8081/v1/accounts/primary/user
curl http://127.0.0.1:8081/v1/accounts/primary/trades
curl http://127.0.0.1:8081/v1/accounts/primary/mirror/api/user
```
