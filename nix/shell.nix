# The development shell. `nix-shell nix/shell.nix` without flakes; the flake's
# devShells.default (`nix develop ./nix`) is this same file. direnv loads
# either one through .envrc.
#
# Everything the package builds with, plus the tools CONTRIBUTING.md asks
# for: gofmt/go vet/go test, Node.js for the graph test, and gjs to run the
# window with `go run ./cmd/agent-radar -app`.
{
  pkgs ? import ./nixpkgs.nix { },
  agent-radar ? import ./default.nix { inherit pkgs; },
}:

pkgs.mkShell {
  inputsFrom = [ agent-radar ];
  packages = [ pkgs.gopls pkgs.nodejs pkgs.gjs ];
}
