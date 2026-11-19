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
  pushing = cfg.prometheus.enable && config.services.prometheus.enable;

  metadata = pkgs.writeText "kasad-metadata.json" (builtins.toJSON {
    inherit (cfg) rooms downstream;
  });

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
      description = "Forget a discovered device after this long without hearing from it.";
    };

    prometheus = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          When true and `services.prometheus.enable` is also true, push every
          reading to Prometheus with remote write and register a scrape job
          for kasad's `/metrics` endpoint (health). Prometheus needs
          `--web.enable-remote-write-receiver` in its `extraFlags`.
        '';
      };
      url = lib.mkOption {
        type = lib.types.str;
        default =
          let p = config.services.prometheus;
          in "http://${if p.listenAddress == "0.0.0.0" || p.listenAddress == "" then "127.0.0.1" else p.listenAddress}:${toString p.port}";
        defaultText = lib.literalExpression ''"http://''${services.prometheus.listenAddress}:''${toString services.prometheus.port}"'';
        description = "Prometheus base URL; readings go to `<url>/api/v1/write`.";
      };
      jobName = lib.mkOption {
        type = lib.types.str;
        default = "kasad";
        description = "Prometheus job_name for the auto-registered scrape.";
      };
      scrapeInterval = lib.mkOption {
        type = lib.types.str;
        default = "60s";
        description = "Scrape interval for /metrics. With push on it only carries health, so it can be longer than `pollInterval`.";
      };
    };

    rooms = lib.mkOption {
      type = lib.types.listOf (lib.types.submodule {
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
      });
      default = [ ];
      example = [{ name = "Kitchen"; devices = [ "Kettle" "Fridge" ]; }];
      description = ''
        Group the cards on the page into rooms, in this order. Devices not
        listed go in a trailing "Other" section. Devices do not know their
        room, so this is configuration.
      '';
    };

    downstream = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = { };
      example = { "Speakers" = "Desk"; };
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
      after = [ "network-online.target" ] ++ lib.optional pushing "prometheus.service";
      wantedBy = [ "multi-user.target" ];

      serviceConfig = sandbox // {
        ExecStart = lib.concatStringsSep " " ([
          "${cfg.package}/bin/kasad"
          "--listen" (lib.escapeShellArg cfg.listen)
          "--broadcast" (lib.escapeShellArg (lib.concatStringsSep "," cfg.broadcast))
          "--poll-interval" cfg.pollInterval
          "--discover-interval" cfg.discoverInterval
          "--expire" cfg.expire
        ] ++ lib.optionals pushing [
          "--prometheus-url" (lib.escapeShellArg cfg.prometheus.url)
          "--job" (lib.escapeShellArg cfg.prometheus.jobName)
        ] ++ lib.optionals (cfg.hosts != [ ]) [
          "--hosts" (lib.escapeShellArg (lib.concatStringsSep "," cfg.hosts))
        ] ++ lib.optionals (cfg.credentialsFile != null) [
          "--credentials" "%d/credentials"
        ] ++ lib.optionals (cfg.rooms != [ ] || cfg.downstream != { }) [
          "--metadata" "${metadata}"
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

    networking.firewall.allowedTCPPorts = lib.optional cfg.openFirewall
      (lib.toInt (lib.last (lib.splitString ":" cfg.listen)));

    # kasad pushes each reading with remote write. The receiver flag is set
    # once by the host (Prometheus refuses a repeated flag, and several
    # exporters push), so only check it is there.
    assertions = [{
      assertion = !pushing || lib.elem "--web.enable-remote-write-receiver" config.services.prometheus.extraFlags;
      message = "services.kasad.prometheus.enable pushes readings: add \"--web.enable-remote-write-receiver\" to services.prometheus.extraFlags.";
    }];

    # Auto-wire a Prometheus scrape job when both services are enabled.
    services.prometheus.scrapeConfigs = lib.mkIf
      (cfg.prometheus.enable && config.services.prometheus.enable) [{
        job_name = cfg.prometheus.jobName;
        scrape_interval = cfg.prometheus.scrapeInterval;
        static_configs = [{ targets = [ cfg.listen ]; }];
      }];
  };
}
