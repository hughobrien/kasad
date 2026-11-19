{
  description = "kasad — Prometheus exporter for TP-Link Kasa smart plugs and switches";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }: let
    systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
    forEachSystem = f: nixpkgs.lib.genAttrs systems f;

    # nixpkgs lags templ; keep this in step with the templ version in go.mod
    # (the *_templ.go files and the drift check use it). Drop when nixpkgs catches up.
    templOverlay = final: prev: {
      templ =
        if prev.lib.versionAtLeast prev.templ.version "0.3.1118" then
          throw "nixpkgs now ships templ ${prev.templ.version}: delete templOverlay from flake.nix"
        else
          prev.templ.overrideAttrs (old: rec {
            version = "0.3.1118";
            src = prev.fetchFromGitHub {
              owner = "a-h";
              repo = "templ";
              rev = "v${version}";
              hash = "sha256-4ARBfwLD37FSCC3pgYWomsXRThl8NkHLte8awD0/Ddo=";
            };
            vendorHash = "sha256-SI3Y7eqw+sLvBmRi7r4Oc30ovIKvFRGxdXF8fyKog9g=";
          });
    };

    perSystem = forEachSystem (system: let
      pkgs = import nixpkgs { inherit system; overlays = [ templOverlay ]; };
      version = "0.1.0";

      kasad-pkg = pkgs.buildGoModule {
        pname = "kasad";
        inherit version;
        src = ./.;
        # When go.mod/go.sum change, set this to pkgs.lib.fakeHash, build, and
        # paste the `got:` hash.
        vendorHash = pkgs.lib.fakeHash;
        subPackages = [ "cmd/kasad" ];
        ldflags = [ "-s" "-w" "-X main.version=${version}" ];
        doCheck = true;
        meta = with pkgs.lib; {
          description = "Prometheus exporter for TP-Link Kasa smart plugs and switches";
          license = licenses.gpl3Plus;
          platforms = platforms.unix;
          mainProgram = "kasad";
        };
      };
    in {
      packages = {
        default = kasad-pkg;
        kasad = kasad-pkg;
      };

      apps.default = {
        type = "app";
        program = "${kasad-pkg}/bin/kasad";
      };

      devShells.default = pkgs.mkShell {
        packages = with pkgs; [ go gopls gotools just templ ];
      };

      checks.gate = kasad-pkg.overrideAttrs (old: {
        pname = "kasad-ci-gate";
        nativeBuildInputs = (old.nativeBuildInputs or [ ]) ++ [ pkgs.clang pkgs.templ ];
        doCheck = true;
        checkPhase = ''
          runHook preCheck
          export HOME="$TMPDIR"
          go vet ./...
          drift=$(gofmt -l . | grep -v '^vendor/' || true)
          [ -z "$drift" ] || { echo "gofmt drift: $drift"; exit 1; }
          go test ./...
          CGO_ENABLED=1 CC=clang go test -race ./...
          cp -a internal/web/templates "$TMPDIR/templ-before"
          templ generate
          diff -rq "$TMPDIR/templ-before" internal/web/templates || { echo "templ drift; run 'just generate'"; exit 1; }
          runHook postCheck
        '';
      });

      formatter = pkgs.nixpkgs-fmt;
    });

    moduleCheck = (nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        self.nixosModules.default
        {
          boot.isContainer = true;
          system.stateVersion = "26.05";
          services.kasad.enable = true;
        }
      ];
    }).config.systemd.units."kasad.service".unit;

    defaultModule = { pkgs, lib, ... }: {
      imports = [ ./nix/module.nix ];
      services.kasad.package = lib.mkDefault
        self.packages.${pkgs.stdenv.hostPlatform.system}.default;
    };
  in {
    nixosModules.default = defaultModule;
    nixosModules.kasad = defaultModule;

    packages  = forEachSystem (system: perSystem.${system}.packages);
    apps      = forEachSystem (system: perSystem.${system}.apps);
    checks    = forEachSystem (system: perSystem.${system}.checks // nixpkgs.lib.optionalAttrs (system == "x86_64-linux") {
      module = moduleCheck;
    });
    devShells = forEachSystem (system: perSystem.${system}.devShells);
    formatter = forEachSystem (system: perSystem.${system}.formatter);
  };
}
