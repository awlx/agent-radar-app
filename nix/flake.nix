{
  description = "Agent Radar — server, web UI and GTK window for Linux without GNOME Shell";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAll (pkgs: rec {
        agent-radar = pkgs.callPackage ./package.nix { };
        default = agent-radar;
      });
      # `nix develop`, or automatically on `cd` through direnv (.envrc).
      # Everything the package builds with, plus the tools CONTRIBUTING.md
      # asks for: gofmt/go vet/go test, Node.js for the graph test, and gjs to
      # run the window with `go run ./cmd/agent-radar -app`.
      devShells = forAll (pkgs: {
        default = pkgs.mkShell {
          inputsFrom = [ self.packages.${pkgs.stdenv.hostPlatform.system}.agent-radar ];
          packages = [ pkgs.gopls pkgs.nodejs pkgs.gjs ];
        };
      });
      checks = forAll (pkgs: import ./checks.nix {
        inherit pkgs;
        agent-radar = self.packages.${pkgs.stdenv.hostPlatform.system}.agent-radar;
        dev-shell = self.devShells.${pkgs.stdenv.hostPlatform.system}.default;
      });
      apps = forAll (pkgs: {
        default = {
          type = "app";
          program = "${self.packages.${pkgs.stdenv.hostPlatform.system}.agent-radar}/bin/agent-radar";
        };
      });
    };
}
