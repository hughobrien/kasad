# SPDX-License-Identifier: GPL-3.0-or-later
#
# NixOS module for kasad.
#
#   {
#     imports = [ inputs.kasad.nixosModules.default ];
#     services.kasad = {
#       enable = true;
#       broadcast = [ "192.168.1.255" ];
#       credentialsFile = "/persist/kasad/credentials"; # newer devices only
#       prometheus.enable = true;
#     };
#   }

{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.services.kasad;

  # The local metrics store, if any: VictoriaMetrics, else Prometheus. Both
  # take remote write at <url>/api/v1/write and answer PromQL at <url>/api/v1.
  vm = config.services.victoriametrics;
  prom = config.services.prometheus;
  store =
    if vm.enable then
      "victoriametrics"
    else if prom.enable then
      "prometheus"
    else
      null;
  storeUrl =
    if store == "victoriametrics" then
      "http://${lib.optionalString (lib.hasPrefix ":" vm.listenAddress) "127.0.0.1"}${vm.listenAddress}"
    else
      let
        host =
          if prom.listenAddress == "0.0.0.0" || prom.listenAddress == "" then
            "127.0.0.1"
          else if prom.listenAddress == "::" || prom.listenAddress == "[::]" then
            "[::1]"
          else if lib.hasInfix ":" prom.listenAddress && !(lib.hasPrefix "[" prom.listenAddress) then
            "[${prom.listenAddress}]"
          else
            prom.listenAddress;
      in
      "http://${host}:${toString prom.port}";
  pushLocal = cfg.prometheus.enable && store != null;
  targets =
    cfg.push // lib.optionalAttrs pushLocal { ${cfg.prometheus.pushName}.url = cfg.prometheus.url; };
  validName = n: builtins.match "[a-zA-Z_][a-zA-Z0-9_]*" n != null;

  # /metrics marks each reading it also pushed with pushed_<name>="true", so
  # the local store's scrape drops the copies it already has; any other
  # scraper keeps them.
  scrapeJob = {
    job_name = cfg.prometheus.jobName;
    scrape_interval = cfg.prometheus.scrapeInterval;
    static_configs = [ { targets = [ cfg.listen ]; } ];
    metric_relabel_configs = lib.optional pushLocal {
      source_labels = [ "pushed_${cfg.prometheus.pushName}" ];
      regex = "true";
      action = "drop";
    };
  };

  metadata = pkgs.writeText "kasad-metadata.json" (
    builtins.toJSON {
      inherit (cfg) rooms downstream;
    }
  );

  # Generic systemd sandboxing, nothing specific to kasad.
  sandbox = {
    NoNewPrivileges = true;
    ProtectSystem = "strict";
    ProtectHome = true;
    PrivateTmp = true;
    PrivateDevices = true;
    ProtectKernelTunables = true;
    ProtectKernelModules = true;
    ProtectKernelLogs = true;
    ProtectControlGroups = true;
    ProtectClock = true;
    ProtectHostname = true;
    ProtectProc = "invisible";
    RestrictNamespaces = true;
    RestrictRealtime = true;
    RestrictSUIDSGID = true;
    LockPersonality = true;
    MemoryDenyWriteExecute = true;
    SystemCallArchitectures = "native";
  };
in
{
  options.services.kasad = {
    enable = lib.mkEnableOption "kasad — Prometheus exporter for TP-Link Kasa devices";

    package = lib.mkOption {
      type = lib.types.package;
      default =
        pkgs.kasad
          or (throw "services.kasad.package is unset. Set it explicitly, e.g. `services.kasad.package = inputs.kasad.packages.\${pkgs.system}.default;`.");
      defaultText = lib.literalExpression "pkgs.kasad";
      description = "The kasad package providing /bin/kasad.";
    };

    listen = lib.mkOption {
      type = lib.types.str;
      default = "127.0.0.1:9881";
      description = "HTTP listen address for the page, /metrics and /healthz.";
    };

    broadcast = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ "255.255.255.255" ];
      description = ''
        IPv4 broadcast addresses to search for devices, for example
        `[ "192.168.1.255" ]`. On a multi-homed host set this explicitly:
        255.255.255.255 goes out of the default route's interface only.
      '';
    };

    hosts = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "IP addresses of devices to poll even if discovery does not find them (other subnets). Use addresses, not DNS names, or a device discovery also finds would show twice. They are never forgotten, so a dead one keeps `kasa_up 0`.";
    };

    allowedHosts = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Host names the page may be reached by, besides IP addresses, localhost and the machine's own hostname (for example a reverse proxy's name). /metrics and /healthz take any Host.";
    };

    credentialsFile = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = ''
        File with the TP-Link cloud account: email on the first line, password
        on the second. Only newer devices (EP25 and similar, KLAP) need it;
        without it those devices are skipped. Read by systemd
        (LoadCredential), so it can be root-only.
      '';
    };

    pollInterval = lib.mkOption {
      type = lib.types.str;
      default = "15s";
      description = "How often to poll each device (a Go duration).";
    };

    discoverInterval = lib.mkOption {
      type = lib.types.str;
      default = "15m";
      description = ''
        How often to broadcast for new devices (a Go duration). A device that
        answers its polls never expires, so this only sets how soon a new
        device, or one with a new address, appears.
      '';
    };

    expire = lib.mkOption {
      type = lib.types.str;
      default = "10m";
      description = ''
        Forget a discovered device after this long without hearing from it,
        checked at each discovery sweep (so a dead device shows `kasa_up 0`
        for `expire` to `expire` plus `discoverInterval`, then its series stop).
        Devices in `hosts` are never forgotten.
      '';
    };

    push = lib.mkOption {
      type = lib.types.attrsOf (
        lib.types.submodule {
          options.url = lib.mkOption {
            type = lib.types.str;
            example = "http://metrics.example:8428";
            description = "Base URL of a remote write receiver; kasad pushes to `<url>/api/v1/write`.";
          };
        }
      );
      default = { };
      example = lib.literalExpression ''{ offsite.url = "http://metrics.example:8428"; }'';
      description = ''
        Remote write targets, by name, each sent every reading. /metrics
        labels each reading it also pushed `pushed_<name>="true"`; the scrape
        job on that target's host should drop those (`metric_relabel_configs`,
        action `drop`), as the one this module registers locally does. Names
        must be letters, digits and `_`, not starting with a digit.
      '';
    };

    prometheus = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          When true and a local metrics store is enabled
          (`services.victoriametrics`, else `services.prometheus`), push to it
          as target `pushName` and register a scrape job for /metrics that
          drops the readings it was pushed. Prometheus needs
          `--web.enable-remote-write-receiver` in its `extraFlags`.
        '';
      };
      pushName = lib.mkOption {
        type = lib.types.str;
        default = lib.replaceStrings [ "-" "." ] [ "_" "_" ] config.networking.hostName;
        defaultText = lib.literalExpression "the host name, with - and . as _";
        description = "Target name for the local store (the `pushed_<name>` label).";
      };
      url = lib.mkOption {
        type = lib.types.str;
        default = storeUrl;
        defaultText = lib.literalExpression ''"http://<listen address of services.victoriametrics or services.prometheus>"'';
        description = "Base URL of the local store.";
      };
      jobName = lib.mkOption {
        type = lib.types.str;
        default = "kasad";
        description = "job_name for the auto-registered scrape, and the job label on pushed series.";
      };
      scrapeInterval = lib.mkOption {
        type = lib.types.str;
        default = "60s";
        description = "Scrape interval for /metrics.";
      };
    };

    rooms = lib.mkOption {
      type = lib.types.listOf (
        lib.types.submodule {
          options = {
            name = lib.mkOption {
              type = lib.types.str;
              description = "Title of the section on the page.";
            };
            devices = lib.mkOption {
              type = lib.types.listOf lib.types.str;
              description = "Device names (as set in the Kasa app; case does not matter).";
            };
          };
        }
      );
      default = [ ];
      example = [
        {
          name = "Kitchen";
          devices = [
            "Kettle"
            "Fridge"
          ];
        }
      ];
      description = ''
        Group the cards on the page into rooms, in this order. Devices not
        listed go in a trailing "Other" section. Devices do not know their
        room, so this is configuration.
      '';
    };

    downstream = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = { };
      example = {
        "Speakers" = "Desk";
      };
      description = ''
        Devices plugged into another metered device, as `name = "upstream name"`.
        The page's combined power skips such a device while its upstream device
        is up, because the upstream reading already includes it.
      '';
    };

    openFirewall = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = ''
        Open the TCP port of `listen` in the firewall, for the page and
        /metrics. Discovery replies come back from UDP 9999 and 20002 and may
        need their own rule on a strict firewall (see the README).
      '';
    };

    extraArgs = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Extra command-line arguments for kasad.";
    };
  };

  config = lib.mkIf cfg.enable {
    users.users.kasad = {
      isSystemUser = true;
      group = "kasad";
      description = "kasad daemon user";
    };
    users.groups.kasad = { };

    systemd.services.kasad = {
      description = "TP-Link Kasa Prometheus exporter";
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ] ++ lib.optional (store != null) "${store}.service";
      wantedBy = [ "multi-user.target" ];

      serviceConfig = sandbox // {
        ExecStart = lib.concatStringsSep " " (
          [
            "${cfg.package}/bin/kasad"
            "--listen"
            (lib.escapeShellArg cfg.listen)
            "--broadcast"
            (lib.escapeShellArg (lib.concatStringsSep "," cfg.broadcast))
            "--poll-interval"
            (lib.escapeShellArg cfg.pollInterval)
            "--discover-interval"
            (lib.escapeShellArg cfg.discoverInterval)
            "--expire"
            (lib.escapeShellArg cfg.expire)
            "--job"
            (lib.escapeShellArg cfg.prometheus.jobName)
          ]
          ++ lib.concatLists (
            lib.mapAttrsToList (n: t: [
              "--push"
              (lib.escapeShellArg "${n}=${t.url}")
            ]) targets
          )
          ++ lib.optionals (cfg.hosts != [ ]) [
            "--hosts"
            (lib.escapeShellArg (lib.concatStringsSep "," cfg.hosts))
          ]
          ++ lib.optionals (cfg.allowedHosts != [ ]) [
            "--allowed-hosts"
            (lib.escapeShellArg (lib.concatStringsSep "," cfg.allowedHosts))
          ]
          ++ lib.optionals (cfg.credentialsFile != null) [
            "--credentials"
            "%d/credentials"
          ]
          ++ lib.optionals (cfg.rooms != [ ] || cfg.downstream != { }) [
            "--metadata"
            "${metadata}"
          ]
          ++ map lib.escapeShellArg cfg.extraArgs
        );
        LoadCredential = lib.optional (cfg.credentialsFile != null) "credentials:${cfg.credentialsFile}";
        User = "kasad";
        Group = "kasad";
        Restart = "on-failure";
        RestartSec = "10s";
        RestrictAddressFamilies = [
          "AF_INET"
          "AF_INET6"
          "AF_UNIX"
        ];
        SystemCallFilter = [
          "@system-service"
          "~@privileged"
        ];
      };
    };

    networking.firewall.allowedTCPPorts = lib.optional cfg.openFirewall (
      lib.toInt (lib.last (lib.splitString ":" cfg.listen))
    );

    assertions = [
      {
        # Prometheus refuses the flag repeated and other exporters push too,
        # so the host sets it once; only check it is there.
        assertion =
          !(pushLocal && store == "prometheus")
          || lib.elem "--web.enable-remote-write-receiver" prom.extraFlags;
        message = "services.kasad.prometheus.enable pushes to Prometheus: add \"--web.enable-remote-write-receiver\" to services.prometheus.extraFlags.";
      }
      {
        assertion = lib.all validName (lib.attrNames targets);
        message = "services.kasad push target names must be letters, digits and _, not starting with a digit: ${lib.concatStringsSep ", " (lib.attrNames targets)}";
      }
    ];

    services.prometheus.scrapeConfigs = lib.mkIf (pushLocal && store == "prometheus") [ scrapeJob ];
    services.victoriametrics.prometheusConfig.scrape_configs = lib.mkIf (
      pushLocal && store == "victoriametrics"
    ) [ scrapeJob ];
  };
}
