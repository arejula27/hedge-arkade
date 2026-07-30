{
  description = "Arkade Hedge — fixed-term BTC-collateralized hedge contract";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
      in
      {
        devShells.default = pkgs.mkShell {
          name = "hedge";

          packages = with pkgs; [
            # Service, contract builder, and covenant tests against the real
            # Arkade VM (github.com/arkade-os/emulator/pkg/arkade)
            go
            gopls
            gotools

            # Client-side contract verifier
            nodejs_22

            just
            git
          ];

          # Only greet an interactive shell: `nix develop --command` is used by
          # the justfile, and stray output there ends up mixed into tool results.
          shellHook = ''
            if [[ $- == *i* ]]; then
              echo "hedge dev shell — go $(go version | cut -d' ' -f3), node $(node --version), just $(just --version | cut -d' ' -f2)"
            fi
          '';
        };
      });
}
