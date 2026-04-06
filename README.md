# stake-cli

`stake-cli` is a focused Go project for working with [Stake](https://hellostake.com) session-token-backed accounts.

It exposes two binaries:

- `stake-cli`, a command-line client that talks directly to the Stake API using locally stored session tokens
- `stake-api-server`, a local proxy that keeps stored sessions warm, serves account metadata, and proxies requests through those sessions

## Features

- XDG-backed local auth store
- Direct CLI access to Stake using stored session tokens
- Browser-first login with optional 1Password-backed credential and MFA entry
- Local proxy and read-only REST API for multi-account access
- Background session keepalive to help short-lived tokens stay usable
- Normalized ASX and US trades from Stake

## Library Usage

The direct Stake client is publicly importable for other tools and libraries:

```go
import "github.com/lox/stake-cli/pkg/stake"
```

## Auth Setup

Preferred browser-first login:

```bash
./dist/stake-cli auth login personal
./dist/stake-cli auth login family-trust
```

That flow opens a visible browser at Stake's login page and lets you complete login and MFA.

If the alias is new, the CLI waits for you to press Enter in the terminal before capturing `localStorage.sessionKey`.

If the alias already exists in the auth store and has a known `user_id`, `auth login` watches for `sessionKey` automatically, validates the browser session, and switches it to that stored account before saving the token. That makes it safer to refresh non-default accounts such as trusts without manually switching in the UI first.

You can also have `auth login` load credentials from 1Password and submit the Stake login form for you.

Service-account auth:

```bash
export OP_SERVICE_ACCOUNT_TOKEN="ops_..."

./dist/stake-cli auth login personal \
  --op-item op://Private/stake.com
```

Desktop-app auth:

```bash
./dist/stake-cli auth login family-trust \
  --op-item op://Private/stake.com \
  --op-account my.1password.com
```

The 1Password-backed flow expects the item to expose Stake credentials through standard fields:

- `username` for the email address
- `password` for the password
- a TOTP field for MFA when the account requires one

The CLI first tries the common built-in TOTP field names, then falls back to scanning the item metadata so custom sections and generated OTP field IDs still work.

The 1Password SDK reports the built-in Private vault as `Personal`. `stake-cli` accepts either vault name when resolving `op://vault/item`, so `op://Private/...` and `op://Personal/...` both work for the default personal vault.

When 1Password automation is enabled, `stake-cli` fills the email and password fields, submits the Angular Material login UI, and enters MFA automatically if Stake prompts for it. If Stake briefly returns to `/auth/login` while the session is still settling, the CLI retries the automated sign-in and keeps waiting for a usable browser session token before saving anything.

Stored auth defaults to:

```text
${XDG_CONFIG_HOME:-~/.config}/stake-cli/accounts.json
```

Fallback manual token capture:

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

The CLI validates the token against `/api/user`, stores the discovered account metadata, and will persist any fresher `Stake-Session-Token` value that later responses return.

Useful auth commands:

```bash
./dist/stake-cli auth list
./dist/stake-cli auth probe primary --interval 30s
./dist/stake-cli auth remove primary
```

`auth probe` keeps validating one stored session until it fails, you interrupt it, or `--max-attempts` is reached. Each probe result is logged to stderr, and the final JSON summary on stdout records how long the token survived, whether Stake rotated it, and the last validation error.

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

To run account operations through `stake-api-server` instead of talking to Stake directly, pass `--proxy`:

```bash
./dist/stake-cli --proxy http://127.0.0.1:8081 user primary
./dist/stake-cli --proxy http://127.0.0.1:8081 trades primary
```

## Server

`stake-api-server` reads the same auth store, periodically validates each stored token, and persists any refreshed token value it sees in response headers.

For proxied Stake requests, send the local account name in `Stake-Session-Token` and the server will swap it for the current stored token for that account before forwarding the request upstream.

```bash
mise trust
mise install
mise run build

./dist/stake-api-server --account primary
```

Proxy example:

```bash
curl \
  -H "Stake-Session-Token: primary" \
  http://127.0.0.1:8081/api/user
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

- `ANY /api/...` proxied to Stake using `Stake-Session-Token: <stored-account-name>`
- `GET /healthz`
- `GET /v1/accounts`
- `GET /v1/accounts/{account}`
- `GET /v1/accounts/{account}/user`
- `GET /v1/accounts/{account}/trades`

Example:

```bash
curl -H "Stake-Session-Token: primary" http://127.0.0.1:8081/api/user
curl http://127.0.0.1:8081/v1/accounts
curl http://127.0.0.1:8081/v1/accounts/primary/user
curl http://127.0.0.1:8081/v1/accounts/primary/trades
```
