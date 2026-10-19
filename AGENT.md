# AGENT.md

This file tells an AI agent how to install, start, and remove `copilot-radar` for a user.

## Install

1. Make sure that Go 1.22 or a later version is installed. Run `go version`.
2. Install the binary:

   ```sh
   go install github.com/awlx/copilot-radar/cmd/copilot-radar@latest
   ```

3. If `go install` cannot get the module, build from the source:

   ```sh
   git clone https://github.com/awlx/copilot-radar /tmp/copilot-radar-src
   cd /tmp/copilot-radar-src && go build -o "$(go env GOPATH)/bin/copilot-radar" ./cmd/copilot-radar
   rm -rf /tmp/copilot-radar-src
   ```

4. Make sure that `$(go env GOPATH)/bin` is in the `PATH` of the user.

## Start

1. Start the server in the background:

   ```sh
   copilot-radar
   ```

2. Open http://127.0.0.1:47900 in a browser.
3. If the port is in use, add `-listen 127.0.0.1:<other port>`.

Do not use `-listen 0.0.0.0:47900` if the user does not ask for LAN access.
If the user asks for LAN access, give the user the URL with the token from the server output.
Do not use `-no-auth`.

## Do a test

Run this command:

```sh
copilot-radar -snapshot
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
   rm "$(go env GOPATH)/bin/copilot-radar"
   ```

## Troubleshoot

- If the page shows no sessions, make sure that `~/.copilot/session-state` exists.
- If the page shows "unauthorized", open the URL with `?token=` from the server output.
- If the page shows "forbidden host", open the page with `127.0.0.1` or `localhost`.
