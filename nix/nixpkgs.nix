# The nixpkgs that flake.lock pins, for Nix without flakes. Both ways build
# from the same commit, and `nix flake update --flake ./nix` moves both.
let
  lock = (builtins.fromJSON (builtins.readFile ./flake.lock)).nodes.nixpkgs.locked;
in
import (fetchTarball {
  url = "https://github.com/${lock.owner}/${lock.repo}/archive/${lock.rev}.tar.gz";
  sha256 = lock.narHash;
})
