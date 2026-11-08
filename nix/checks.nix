# `nix flake check ./nix` builds all of these; without flakes,
# `nix-build nix/checks.nix` does.
{
  pkgs ? import ./nixpkgs.nix { },
  agent-radar ? import ./default.nix { inherit pkgs; },
  dev-shell ? import ./shell.nix { inherit pkgs agent-radar; },
}:

{
  # The package itself. Fails first when go.mod changed and vendorHash did not.
  package = agent-radar;

  # The shell that `nix develop ./nix` and direnv (.envrc) load.
  inherit dev-shell;

  # What CONTRIBUTING.md asks for, offline in the sandbox. The package build
  # only tests cmd/agent-radar; this runs every Go and Node test of the repo.
  tests = agent-radar.overrideAttrs (old: {
    pname = "agent-radar-tests";
    nativeCheckInputs = (old.nativeCheckInputs or [ ]) ++ [ pkgs.nodejs ];
    doCheck = true;
    checkPhase = ''
      runHook preCheck
      unformatted=$(find . -name "*.go" -not -path "./vendor/*" -exec gofmt -l {} +)
      if [ -n "$unformatted" ]; then
        echo "gofmt: these files are not formatted:"; echo "$unformatted"; exit 1
      fi
      go vet ./...
      go test ./...
      node --test internal/server/ui/graph.test.mjs \
        linux/extension/status.test.mjs linux/extension/startup.test.mjs
      runHook postCheck
    '';
  });

  # The window imports Gtk 4, Soup 3 and WebKit 6 before it opens a display.
  # With no display, a correct wrapper gets that far and fails on the display;
  # a broken one fails earlier because gjs or a typelib is missing.
  window-wrapper = pkgs.runCommand "agent-radar-window-wrapper" { } ''
    export HOME=$TMPDIR
    unset DISPLAY WAYLAND_DISPLAY
    log=$TMPDIR/log
    PATH= ${agent-radar}/bin/agent-radar -app -demo -listen 127.0.0.1:47999 >$log 2>&1 || true
    cat $log
    if grep -q 'Typelib file\|executable file not found\|JS ERROR' $log; then
      echo "the window could not load gjs or its typelibs"; exit 1
    fi
    grep -q 'Failed to open display' $log
    touch $out
  '';
}
