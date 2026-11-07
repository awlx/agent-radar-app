# Remote Hermes Agent

Agent Radar can show the sessions of a [Hermes Agent](https://github.com/NousResearch/hermes-agent) that runs on a different computer.
This function is optional. It is off until you set a Hermes URL.

## What the radar shows

| Item | Source in Hermes |
|------|------------------|
| Sessions of the last 24 hours, maximum 20 | `GET /api/sessions` |
| Session name, platform (for example `cli`, `cron`, `matrix`), model, folder | The session row |
| Last request and last answer | `GET /api/sessions/{id}/messages` |
| Running tools and data destinations | Tool calls without a result |
| "Waiting for you" | An open `clarify` tool call |
| Sub-agents of `delegate_task` | Session rows with the source `subagent` |
| Tokens and model calls | `input_tokens`, `output_tokens`, `api_call_count` |

The radar shows Hermes sessions with the orange color, the 🌐 Hermes label, and `remote · <host>`.
Destinations that stay on the Hermes computer get the host name, for example `hermes: Filesystem`.
The "Show app" button is not available for Hermes sessions.

The radar only reads. It does not send prompts, commands, or approvals to Hermes.

## Set up

1. Find the URL of the Hermes dashboard (default port `9119`) or of the Hermes API server (default port `8642`).
   The radar finds the server type automatically.
2. Make the file `hermes.json` in the Agent Radar config folder:

   | System | File |
   |--------|------|
   | macOS | `~/Library/Application Support/agent-radar/hermes.json` |
   | Linux | `~/.config/agent-radar/hermes.json` |
   | Windows | `%APPDATA%\agent-radar\hermes.json` |

   For the dashboard with a password login:

   ```json
   {"url": "https://hermes.example.com", "username": "user", "password": "..."}
   ```

   For the API server, or for a dashboard session token:

   ```json
   {"url": "https://hermes.example.com:8642", "token": "..."}
   ```

3. On macOS and Linux, other users must not read the file. If they can, the radar does not use the file and writes an error to the log:

   ```sh
   chmod 600 ~/Library/Application\ Support/agent-radar/hermes.json
   ```

4. Start or restart Agent Radar.

To test the connection, run:

```sh
agent-radar -snapshot -all
```

The Hermes sessions have `"surface": "Hermes"` and a `remote` field.

## Settings

| Setting | File key | Variable | Flag |
|---------|----------|----------|------|
| URL | `url` | `AGENT_RADAR_HERMES_URL` | `-hermes` |
| Bearer token | `token` | `AGENT_RADAR_HERMES_TOKEN` | – |
| User name | `username` | `AGENT_RADAR_HERMES_USER` | – |
| Password | `password` | `AGENT_RADAR_HERMES_PASSWORD` | – |
| Hermes profile | `profile` | – | `?profile=` in the URL |

A variable overrides the file. The flag overrides the variable.
The URL can be a dashboard page, for example `https://hermes.example.com/sessions?profile=work`.
The radar uses the scheme, the host, the port, and the profile.

If you set a token, the radar does not use the password.

## Authentication

| Server | Method |
|--------|--------|
| Dashboard with password login | The radar signs in with `POST /auth/password-login` and keeps the session cookie in memory. When the cookie expires, it signs in again. |
| Dashboard with `HERMES_DASHBOARD_SESSION_TOKEN` | `Authorization: Bearer <token>` |
| API server with `API_SERVER_KEY` | `Authorization: Bearer <token>` |

## Security

- Use HTTPS. With `http://` to a different computer, the password goes over the network without encryption. The radar writes a warning to the log.
- The radar checks the TLS certificate. It does not have an option to skip this check.
- The radar does not follow redirects. Thus the credentials do not go to a different host.
- Log messages and errors do not contain the token or the password.
- Hermes sessions can contain private messages and commands. The `/state` and `/events` endpoints of the radar contain this data. Keep the radar on `127.0.0.1`, or use the LAN token.

## Limits

- The radar reads Hermes every 5 seconds. After an error, it waits longer, up to 1 minute.
- The radar reads the messages of at most 10 recent sessions and 8 running sub-agents in each cycle.
- A tool call without a result for more than 30 minutes does not show as running.
- Live sub-agent details from the Hermes WebSocket are not available. Sub-agents show only when Hermes keeps them as session rows.
- A continued session (after a reset or a compression) shows as a separate session.

## Troubleshoot

| Log message | Cause | Fix |
|-------------|-------|-----|
| `not authorized` | The token, user name, or password is wrong. | Correct `hermes.json`. |
| `certificate is valid for ...` | The HTTPS server sends a certificate for a different name. | Install a correct certificate on the Hermes host. |
| `not a Hermes dashboard or API server` | The URL is wrong. | Use the base URL of the dashboard or API server. |
| `other users can read this file` | The file mode is too open. | Run `chmod 600` on the file. |
| `too many login attempts` | Hermes limits the login rate. | Wait, then restart the radar. |
