{
  description = "kasad — Prometheus exporter for TP-Link Kasa smart plugs and switches";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }: let
    systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
    forEachSystem = f: nixpkgs.lib.genAttrs systems f;

    perSystem = forEachSystem (system: let
      pkgs = import nixpkgs { inherit system; };
      version = "0.1.0";

      kasad-pkg = pkgs.buildGoModule {
        pname = "kasad";
        inherit version;
        src = ./.;
        # When go.mod/go.sum change, set this to pkgs.lib.fakeHash, build, and
        # paste the `got:` hash.
        vendorHash = "sha256-MP1q99k5Vpi89ZYgDxcxzeL6KvOyNsIdwA7GEHgsGYA=";
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
        packages = with pkgs; [ go gopls gotools just ];
      };

      checks.gate = kasad-pkg.overrideAttrs (old: {
        pname = "kasad-ci-gate";
        nativeBuildInputs = (old.nativeBuildInputs or [ ]) ++ [ pkgs.clang ];
        doCheck = true;
        checkPhase = ''
          runHook preCheck
          export HOME="$TMPDIR"
          go vet ./...
          drift=$(gofmt -l . || true)
          [ -z "$drift" ] || { echo "gofmt drift: $drift"; exit 1; }
          go test ./...
          CGO_ENABLED=1 CC=clang go test -race ./...
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
