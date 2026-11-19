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

{ config, lib, pkgs, ... }:

let
  cfg = config.services.kasad;

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
in {
  options.services.kasad = {
    enable = lib.mkEnableOption "kasad — Prometheus exporter for TP-Link Kasa devices";

    package = lib.mkOption {
      type = lib.types.package;
      default = pkgs.kasad or (throw
        "services.kasad.package is unset. Set it explicitly, e.g. `services.kasad.package = inputs.kasad.packages.\${pkgs.system}.default;`.");
      defaultText = lib.literalExpression "pkgs.kasad";
      description = "The kasad package providing /bin/kasad.";
    };

    listen = lib.mkOption {
      type = lib.types.str;
      default = "127.0.0.1:9881";
      description = "HTTP listen address for /metrics.";
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
      description = "Devices to poll even if discovery does not find them (other subnets).";
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
      default = "1m";
      description = "How often to broadcast for new devices (a Go duration).";
    };

    expire = lib.mkOption {
      type = lib.types.str;
      default = "10m";
      description = "Forget a discovered device after this long without hearing from it.";
    };

    prometheus = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          When true and `services.prometheus.enable` is also true, register
          a scrape job for kasad's `/metrics` endpoint automatically.
        '';
      };
      jobName = lib.mkOption {
        type = lib.types.str;
        default = "kasad";
        description = "Prometheus job_name for the auto-registered scrape.";
      };
      scrapeInterval = lib.mkOption {
        type = lib.types.str;
        default = "60s";
        description = "Scrape interval. kasad serves cached readings, so this can be longer than `pollInterval`.";
      };
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
      after = [ "network-online.target" ];
      wantedBy = [ "multi-user.target" ];

      serviceConfig = sandbox // {
        ExecStart = lib.concatStringsSep " " ([
          "${cfg.package}/bin/kasad"
          "--listen" (lib.escapeShellArg cfg.listen)
          "--broadcast" (lib.escapeShellArg (lib.concatStringsSep "," cfg.broadcast))
          "--poll-interval" cfg.pollInterval
          "--discover-interval" cfg.discoverInterval
          "--expire" cfg.expire
        ] ++ lib.optionals (cfg.hosts != [ ]) [
          "--hosts" (lib.escapeShellArg (lib.concatStringsSep "," cfg.hosts))
        ] ++ lib.optionals (cfg.credentialsFile != null) [
          "--credentials" "%d/credentials"
        ] ++ map lib.escapeShellArg cfg.extraArgs);
        LoadCredential = lib.optional (cfg.credentialsFile != null)
          "credentials:${cfg.credentialsFile}";
        User = "kasad";
        Group = "kasad";
        Restart = "on-failure";
        RestartSec = "10s";
        RestrictAddressFamilies = [ "AF_INET" "AF_INET6" "AF_UNIX" ];
        SystemCallFilter = [ "@system-service" "~@privileged" ];
      };
    };

    # Auto-wire a Prometheus scrape job when both services are enabled.
    services.prometheus.scrapeConfigs = lib.mkIf
      (cfg.prometheus.enable && config.services.prometheus.enable) [{
        job_name = cfg.prometheus.jobName;
        scrape_interval = cfg.prometheus.scrapeInterval;
        static_configs = [{ targets = [ cfg.listen ]; }];
      }];
  };
}
