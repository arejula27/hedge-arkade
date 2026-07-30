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
            # Covenant tests run against the real Arkade VM (../emulator/pkg/arkade)
            go
            gopls
            gotools

            # Contract Program objects + web service
            nodejs_22

            git
          ];

          shellHook = ''
            echo "hedge dev shell — go $(go version | cut -d' ' -f3), node $(node --version)"
          '';
        };
      });
}
