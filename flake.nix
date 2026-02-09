{
  description = "Go HTTP server development environment";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs {
          inherit system;
        };
      in
      {
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            delve
            gotools
            golangci-lint
            air
            curl
          ];

          shellHook = ''
            export CGO_ENABLED=1
            export GOPATH="$PWD/.gopath"
            export GOMODCACHE="$PWD/.gopath/pkg/mod"
            export PATH="$GOPATH/bin:$PATH"

            echo "Go HTTP server dev shell ready"
            echo "Go: $(go version)"
          '';
        };
      }
    );
}
