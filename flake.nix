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
        agent-radar = pkgs.callPackage ./nix/package.nix { };
        default = agent-radar;
      });
      apps = forAll (pkgs: {
        default = {
          type = "app";
          program = "${self.packages.${pkgs.system}.agent-radar}/bin/agent-radar";
        };
      });
    };
}
