# Contributing

Thank you for your help. Issues and pull requests are welcome.

## Before you open a pull request

1. Run `gofmt -l .`. The command must show no files.
2. Run `go vet ./...` and `go test ./...`.
3. If you change the macOS app, run `./macos/build-app.sh` on a Mac.
4. If you change the Windows app, run `./windows/build-app.sh` and `GOOS=windows go vet ./...`. The CI does a smoke test of the Windows app.
5. If you change the UI, add a screenshot from `copilot-radar -demo`.
6. If you change the graph, run `node --test internal/server/ui/graph.test.mjs` with Node.js 18 or later.

## Rules

- Do not add data from real sessions to tests, issues, or screenshots. Use mock data.
- Do not send data to the network. The radar reads only local files.
- Do not show command arguments in the `-snapshot` output or in `/api/snapshot`. Arguments can contain credentials.
- Use third-party Go modules only for the Windows app. Keep the other parts free of third-party Go modules.
