{
  description = "quipu: track git worktrees and Claude Code sessions";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs =
    { self, nixpkgs }:
    let
      supportedSystems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems =
        f: nixpkgs.lib.genAttrs supportedSystems (system: f system nixpkgs.legacyPackages.${system});
    in
    {
      # Overlay that adds `quipu` to a nixpkgs instance, for consumers who
      # prefer composing it into their own package set.
      overlays.default = final: _prev: {
        quipu = final.callPackage ./nix/package.nix { };
      };

      # `nix build`, `nix build .#quipu`.
      packages = forAllSystems (
        _system: pkgs: rec {
          quipu = pkgs.callPackage ./nix/package.nix { };
          default = quipu;
        }
      );

      # `nix run` -> `quipu`.
      apps = forAllSystems (
        system: _pkgs: rec {
          quipu = {
            type = "app";
            program = nixpkgs.lib.getExe self.packages.${system}.quipu;
            meta.description = "Track git worktrees and Claude Code sessions";
          };
          default = quipu;
        }
      );

      devShells = forAllSystems (
        _system: pkgs: {
          default = pkgs.mkShellNoCC {
            packages = [
              pkgs.go_1_26
              pkgs.golangci-lint
              pkgs.goreleaser
            ];
          };
        }
      );

      formatter = forAllSystems (_system: pkgs: pkgs.nixfmt);
    };
}
