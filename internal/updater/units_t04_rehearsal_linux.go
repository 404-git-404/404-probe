//go:build linux && t04_rehearsal

package updater

// The isolated T04 receipt worker uses its fixture CA file override. This
// build-tagged unit is separate from production and does not modify host trust.
const removalWorkerUnitContents = `[Unit]
Description=404-probe Agent removal receipt worker
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=30min
StartLimitBurst=10

[Service]
Type=oneshot
User=root
Group=root
UMask=0077
ExecStart=/usr/local/lib/404-probe-t04-agent/.404-probe-agent-removal-worker updater removal-worker
Environment=SSL_CERT_FILE=/usr/local/lib/404-probe-t04-control/t04-root-ca.pem
Restart=on-failure
RestartSec=30s
TimeoutStartSec=5min
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
ReadWritePaths=/var/lib/404-probe-t04-agent-updater /etc/systemd/system

[Install]
WantedBy=multi-user.target
`

const removalFinalizerUnitContents = `[Unit]
Description=404-probe fixed local Agent removal finalizer
After=local-fs.target
StartLimitIntervalSec=10min
StartLimitBurst=5

[Service]
Type=oneshot
User=root
Group=root
UMask=0077
ExecStart=/usr/local/lib/404-probe-t04-agent/.404-probe-agent-removal-worker updater removal-finalize
Restart=on-failure
RestartSec=30s
TimeoutStartSec=10min
NoNewPrivileges=true
PrivateTmp=true
PrivateNetwork=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictAddressFamilies=AF_UNIX
ReadWritePaths=/etc /var/lib /run /usr/local/lib

[Install]
WantedBy=multi-user.target
`
