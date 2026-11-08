# Nix without flakes: `nix-build nix` builds the package into ./result.
{ pkgs ? import ./nixpkgs.nix { } }:

pkgs.callPackage ./package.nix { }
