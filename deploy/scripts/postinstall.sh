#!/bin/bash
set -e

# Create system user and group if they don't exist.
if ! getent group spanbarn > /dev/null 2>&1; then
    groupadd --system spanbarn
fi
if ! getent passwd spanbarn > /dev/null 2>&1; then
    useradd --system --gid spanbarn --no-create-home \
        --home-dir /var/lib/spanbarn \
        --shell /usr/sbin/nologin \
        --comment "SpanBarn service account" spanbarn
fi

# Ensure state directory exists with correct ownership.
install -d -o spanbarn -g spanbarn -m 0750 /var/lib/spanbarn
install -d -o spanbarn -g spanbarn -m 0750 /var/lib/spanbarn/spool

# Ensure config directory exists.
install -d -m 0755 /etc/spanbarn

# Drop a sample config if no config exists yet.
if [ ! -f /etc/spanbarn/spanbarn.conf ]; then
    cp /etc/spanbarn/spanbarn.conf.example /etc/spanbarn/spanbarn.conf
    chmod 0640 /etc/spanbarn/spanbarn.conf
    chown root:spanbarn /etc/spanbarn/spanbarn.conf
    echo "SpanBarn: sample config installed at /etc/spanbarn/spanbarn.conf — edit before starting."
fi

# Reload systemd and enable the service.
if command -v systemctl > /dev/null 2>&1; then
    systemctl daemon-reload
    systemctl enable spanbarn.service || true
    echo "SpanBarn: service enabled. Start with: sudo systemctl start spanbarn"
fi
