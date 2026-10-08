//go:build linux && !t04_rehearsal

package updater

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
ExecStart=` + removalWorkerBinary + ` updater removal-worker
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
ReadWritePaths=` + removalWorkerWritablePaths + `

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
ExecStart=` + removalWorkerBinary + ` updater removal-finalize
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
ReadWritePaths=` + removalFinalizerWritablePaths + `

[Install]
WantedBy=multi-user.target
`
