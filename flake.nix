{
  description = "kasad — Prometheus exporter for TP-Link Kasa smart plugs and switches";

  inputs = {
    ci.url = "git+https://forgejo.wg/hughobrien/ci.git?ref=main";
    nixpkgs.follows = "ci/nixpkgs";
  };

  outputs =
    {
      self,
      ci,
      nixpkgs,
    }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
      ];
      forEachSystem = f: nixpkgs.lib.genAttrs systems f;

      # nixpkgs lags templ; keep this in step with the templ version in go.mod
      # (the *_templ.go files and the drift check use it). Drop when nixpkgs catches up.
      templOverlay = _final: prev: {
        templ =
          if prev.lib.versionAtLeast prev.templ.version "0.3.1118" then
            throw "nixpkgs now ships templ ${prev.templ.version}: delete templOverlay from flake.nix"
          else
            prev.templ.overrideAttrs (_old: rec {
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

      moduleSystem =
        extra:
        nixpkgs.lib.nixosSystem {
          system = "x86_64-linux";
          modules = [
            self.nixosModules.default
            {
              boot.isContainer = true;
              system.stateVersion = "26.05";
              networking.hostName = "my-host";
              services.kasad = {
                enable = true;
                openFirewall = true;
                rooms = [
                  {
                    name = "Kitchen";
                    devices = [ "Kettle" ];
                  }
                ];
                downstream = {
                  "Speakers" = "Desk";
                };
                prometheus.enable = true;
                push.offsite.url = "http://offsite:8428";
                allowedHosts = [ "kasad.example" ];
              };
            }
            extra
          ];
        };

      # Evaluation fails (so the check does) if the options stop doing their job.
      moduleCheck =
        let
          vm = (moduleSystem { services.victoriametrics.enable = true; }).config;
          prom =
            (moduleSystem {
              services.prometheus.enable = true;
              services.prometheus.extraFlags = [ "--web.enable-remote-write-receiver" ];
            }).config;
          inherit (nixpkgs) lib;
          job = jobs: lib.findFirst (j: j.job_name == "kasad") null jobs;
          drops =
            j:
            lib.any (
              r: r.source_labels == [ "pushed_my_host" ] && r.regex == "true" && r.action == "drop"
            ) j.metric_relabel_configs;
        in
        assert lib.hasInfix "my_host=http://127.0.0.1:8428"
          vm.systemd.services.kasad.serviceConfig.ExecStart;
        assert lib.hasInfix "offsite=http://offsite:8428" vm.systemd.services.kasad.serviceConfig.ExecStart;
        assert drops (job vm.services.victoriametrics.prometheusConfig.scrape_configs);
        assert vm.services.prometheus.scrapeConfigs == [ ];
        assert lib.hasInfix "my_host=http://127.0.0.1:9090"
          prom.systemd.services.kasad.serviceConfig.ExecStart;
        assert drops (job prom.services.prometheus.scrapeConfigs);
        assert builtins.elem 9881 vm.networking.firewall.allowedTCPPorts;
        assert lib.hasInfix "--metadata" vm.systemd.services.kasad.serviceConfig.ExecStart;
        assert lib.hasInfix "--allowed-hosts" vm.systemd.services.kasad.serviceConfig.ExecStart;
        assert lib.hasInfix "kasad.example" vm.systemd.services.kasad.serviceConfig.ExecStart;
        vm.systemd.units."kasad.service".unit;

      defaultModule =
        { pkgs, lib, ... }:
        {
          imports = [ ./nix/module.nix ];
          services.kasad.package = lib.mkDefault self.packages.${pkgs.stdenv.hostPlatform.system}.default;
        };

      perSystem = forEachSystem (
        system:
        let
          pkgs = import nixpkgs {
            inherit system;
            overlays = [ templOverlay ];
          };
          ciLib = ci.lib.forSystem system;
          version = "0.1.0";

          kasad-pkg = pkgs.buildGo127Module {
            pname = "kasad";
            inherit version;
            src = ./.;
            # When go.mod/go.sum change, set this to pkgs.lib.fakeHash, build, and
            # paste the `got:` hash.
            vendorHash = "sha256-jlcmrpCImTBUHdA+wSrmOaVQPzDeMx9hY5o4+jRdyLA=";
            subPackages = [ "cmd/kasad" ];
            ldflags = [
              "-s"
              "-w"
              "-X main.version=${version}"
            ];
            doCheck = true;
            meta = with pkgs.lib; {
              description = "Prometheus exporter for TP-Link Kasa smart plugs and switches";
              license = licenses.gpl3Plus;
              platforms = platforms.unix;
              mainProgram = "kasad";
            };
          };

          goGate = kasad-pkg.overrideAttrs (old: {
            pname = "kasad-go-gate";
            nativeBuildInputs = (old.nativeBuildInputs or [ ]) ++ [
              pkgs.clang
              pkgs.templ
            ];
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
              diff -rq "$TMPDIR/templ-before" internal/web/templates || { echo "templ drift; run 'nix develop -c templ generate'"; exit 1; }
              runHook postCheck
            '';
          });

          lint = ciLib.lint { inherit self; };
        in
        {
          packages = {
            default = kasad-pkg;
            kasad = kasad-pkg;
          };

          apps.default = {
            type = "app";
            program = pkgs.lib.getExe kasad-pkg;
            inherit (kasad-pkg) meta;
          };

          devShells.default = pkgs.mkShell {
            packages =
              (with pkgs; [
                go_1_27
                gopls
                gotools
                templ
                clang
              ])
              ++ ciLib.devShellPackages;
            env = ciLib.devShellEnv // {
              GOTOOLCHAIN = "local";
            };
          };

          checks = {
            gate = pkgs.symlinkJoin {
              name = "gate";
              paths = [
                lint
                goGate
              ]
              ++ pkgs.lib.optional (system == "x86_64-linux") moduleCheck;
            };
          };

          formatter = pkgs.nixfmt;
        }
      );
    in
    {
      nixosModules.default = defaultModule;
      nixosModules.kasad = defaultModule;

      packages = forEachSystem (system: perSystem.${system}.packages);
      apps = forEachSystem (system: perSystem.${system}.apps);
      checks = forEachSystem (
        system:
        perSystem.${system}.checks
        // nixpkgs.lib.optionalAttrs (system == "x86_64-linux") {
          module = moduleCheck;
        }
      );
      devShells = forEachSystem (system: perSystem.${system}.devShells);
      formatter = forEachSystem (system: perSystem.${system}.formatter);
    };
}
