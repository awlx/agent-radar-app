# Remote Hermes Agent

Agent Radar can show the sessions of a [Hermes Agent](https://github.com/NousResearch/hermes-agent) that runs on a different computer.
This function is optional. It is off until you set a Hermes URL.

## What the radar shows

| Item | Source in Hermes |
|------|------------------|
| Sessions of the last 24 hours, maximum 20. Working sessions come first. | `GET /api/sessions` |
| Running cron jobs, with the name `cron · <job>` (dashboard only) | `GET /api/cron/jobs`, field `latest_execution` |
| Session name, platform (for example `cli`, `cron`, `matrix`), model, folder | The session row |
| Last request and last answer | `GET /api/sessions/{id}/messages` |
| Running tools and data destinations | Tool calls without a result |
| "Waiting for you" | An open `clarify` tool call |
| Background delegates of `delegate_task`, with goal and model | The parent messages: a `delegate_task` result with `"status": "dispatched"` starts them. The `[ASYNC DELEGATION COMPLETE — <id>]` message ends them. |
| Delegate model | The child session of the delegate. Else the model of the task, the model of the selected delegation backend option, or `delegation.model`, from `GET /api/config` (dashboard only). Else the model of the parent session. Without the config, the model is `unknown`. |
| Delegate tools, data flows and tokens | The child session of the delegate (dashboard only). `GET /api/sessions/search?source=subagent` finds it by the goal text. `GET /api/sessions/{id}` and its messages give the model, the tokens and the tool calls. Between tool calls, the delegate shows `thinking` and its last tool. The parent session also shows these tools and data flows. |
| Sub-agents with session rows | Session rows with the source `subagent` |
| Tokens and model calls | `input_tokens`, `output_tokens`, `api_call_count` |

The radar shows Hermes sessions with the orange color, the 🌐 Hermes label, and `remote · <host>`.
Destinations that stay on the Hermes computer get the host name, for example `hermes: Filesystem`.
The "Show app" button is not available for Hermes sessions.

The radar only reads. It does not send prompts, commands, or approvals to Hermes.

MCP calls use `MCP: <server>` destinations when the recorded name has the
Hermes `mcp__<server>__<tool>` format. This identifies the encoded server label,
not its network address. Legacy or incomplete names show **MCP: unknown server**.
Tool description requests use separate **Tool discovery** destinations. They do not
show the described tools as executed calls. Descriptions and deferred-call batches
show readable tool names; hover over their activity text or inspect the tool details
to see the recorded arguments and exact identifiers.

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

5. On macOS, if the Hermes host is on your local network, macOS asks for the Local Network permission. Select **Allow**. To change it later, open **System Settings > Privacy & Security > Local Network**.

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
- The radar reads the messages of at most 10 sessions and 8 running sub-agents in each cycle. Sessions with running delegates come first.
- A tool call without a result for more than 30 minutes does not show as running.
- A session with running background delegates shows as working. Each delegate shows as a sub-agent of its parent session.
- Hermes keeps delegate sessions out of `GET /api/sessions`. The WebSocket method `subagent.list` gives data only to the client that owns the session. Thus the radar uses the search route to find the child session of a delegate. If the search does not find it, the delegate shows "background task" and no tools. The API server has no search route.
- The radar does 4 searches for new delegates at most in each cycle.
- If a backend option has fallback providers, the real model can be different. The child session shows the real model when the radar finds it.
- A background delegate without a completion message for 6 hours does not show as running. A delegate of an ended session does not show.
- The radar reads `GET /api/config` at most one time in 5 minutes. It keeps only the model names of the `delegation` settings. It does not keep other config values.
- A continued session (after a reset or a compression) shows as a separate session.
- A cron job shows as working only while its run is in progress. Short runs can end between two reads. A run without an end after 2 hours does not show as working.
- The radar does not use the Hermes `is_active` field. Hermes sets it for 5 minutes after the last activity, also when the session waits for the user.
- The radar does not show empty cron sessions that Hermes did not close (no title and no messages).

## Troubleshoot

| Log message | Cause | Fix |
|-------------|-------|-----|
| `not authorized` | The token, user name, or password is wrong. | Correct `hermes.json`. |
| `certificate is valid for ...` | The HTTPS server sends a certificate for a different name. | Install a correct certificate on the Hermes host. |
| `not a Hermes dashboard or API server` | The URL is wrong. | Use the base URL of the dashboard or API server. |
| `other users can read this file` | The file mode is too open. | Run `chmod 600` on the file. |
| `too many login attempts` | Hermes limits the login rate. | Wait, then restart the radar. |
| `no route to host` (macOS app only) | macOS blocks access to the local network. | Allow Agent Radar in **System Settings > Privacy & Security > Local Network**. |
