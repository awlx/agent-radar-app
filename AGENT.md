# AGENT.md

This file tells an AI agent how to install, start, and remove `agent-radar` for a user.

## Install

1. Make sure that Go 1.22 or a later version is installed. Run `go version`.
2. Install the binary:

   ```sh
   go install github.com/awlx/agent-radar-app/cmd/agent-radar@latest
   ```

3. If `go install` cannot get the module, build from the source:

   ```sh
   git clone https://github.com/awlx/agent-radar-app /tmp/agent-radar-src
   cd /tmp/agent-radar-src && go build -o "$(go env GOPATH)/bin/agent-radar" ./cmd/agent-radar
   rm -rf /tmp/agent-radar-src
   ```

4. Make sure that `$(go env GOPATH)/bin` is in the `PATH` of the user.

## Start

For a native GNOME top bar and desktop window on Linux, follow [Linux and GNOME](docs/linux.md).
Check the runtime libraries. If `dist/linux/agent-radar` is not available, build with
`sh linux/build-app.sh`. Install with `sh linux/install.sh`.
The Linux bundle includes the binary, installer, and [Fedora commands](docs/linux.md#fedora-workstation).
For an already loaded Shell extension, installing changed JavaScript does not update
the running module. Do not claim the new icon is visible without checking a fresh Shell session.
Launch **Agent Radar** or run `~/.local/bin/agent-radar-gnome`.
The app manages its backend; no service or `systemctl` step is needed.
Closing the window hides it. Use **Quit Radar** to stop the app-owned backend.
An external compatible server can be reused, but the app must not stop it on Quit.
Do not enable login autostart, install system packages, or restart the desktop without the user's decision.

1. Start the server. The command runs in the foreground until you stop it with Ctrl+C.
   To keep the server running, start it in a separate terminal or as a background task:

   ```sh
   agent-radar
   ```

2. Open http://127.0.0.1:47900 in a browser.
3. If the port is in use, add `-listen 127.0.0.1:<other port>`.

Do not use `-listen 0.0.0.0:47900` if the user does not ask for LAN access.
If the user asks for LAN access, give the user the URL with the token from the server output.
Do not use `-no-auth`.

## Do a test

Run this command:

```sh
agent-radar -snapshot
```

The output is a JSON summary of the active agents.
The output contains session names, folders, repositories, tool names, and data destinations.
The output does not contain command arguments, because arguments can contain credentials.
Show only the counts to the user if the user does not ask for more.

## Update

Do the install procedure again.

## Remove

1. Stop the server.
2. Remove the binary:

   ```sh
   rm "$(go env GOPATH)/bin/agent-radar"
   ```

## Troubleshoot

- If the page shows no sessions, make sure that `~/.copilot/session-state` exists.
- If the page shows "unauthorized", open the URL with `?token=` from the server output.
- If the page shows "forbidden host", open the page with `127.0.0.1` or `localhost`.
