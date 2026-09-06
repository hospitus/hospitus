# HOSPITUS Manifest Examples

This directory contains a collection of example manifest templates for various workloads, categorized by type.

## Structure

Every manifest is organized as follows:
`manifests/<category>/<workload>/template.toml`

Each workload directory also contains a `test.sh` script for automated validation.

## Quick Start

```bash
# Apply a template
hospitus apply --start manifests/core-infra/simple-jail/template.toml

# Apply with custom variables
hospitus apply --start --var name=my-webserver manifests/web-hosting/nginx/template.toml
```

## Available Templates

### AI & Compute
| Workload | Description |
|----------|-------------|
| [localai](manifests/ai-compute/localai/) | LocalAI - Self-hosted, community-driven, local OpenAI-compatible API |
| [ollama](manifests/ai-compute/ollama/) | Ollama - Local LLM Runner with GPU Passthrough (bhyve) |

### Analytics
| Workload | Description |
|----------|-------------|
| [matomo](manifests/analytics/matomo/) | Matomo - The leading open-source web analytics platform |
| [plausible](manifests/analytics/plausible/) | Plausible Analytics - Simple, open-source, lightweight and privacy-friendly web analytics |

### Automation & IoT
| Workload | Description |
|----------|-------------|
| [mosquitto](manifests/automation/mosquitto/) | Mosquitto - An open source MQTT broker |
| [n8n](manifests/automation/n8n/) | n8n - Free and source-available fair-code licensed workflow automation tool |
| [node-red](manifests/automation/node-red/) | Node-RED - Low-code programming for event-driven applications |

### Collaboration
| Workload | Description |
|----------|-------------|
| [inspircd](manifests/collaboration/inspircd/) | InspIRCd - A modular C++ IRC Daemon |
| [mail](manifests/collaboration/mail/) | Native FreeBSD Mail Stack - Postfix MTA and Dovecot IMAP/POP3 |
| [matrix-synapse](manifests/collaboration/matrix-synapse/) | Matrix Synapse - Reference homeserver for the Matrix protocol |
| [moodle](manifests/collaboration/moodle/) | Moodle - The world's most popular learning management system |
| [nextcloud](manifests/collaboration/nextcloud/) | Nextcloud Hub - Content Collaboration Platform |
| [pleroma](manifests/collaboration/pleroma/) | Pleroma - Lightweight federated social networking server |
| [wikijs](manifests/collaboration/wikijs/) | Wiki.js - The most powerful and extensible open source Wiki software |

### Containers (Podman)
| Workload | Description |
|----------|-------------|
| [busybox](manifests/containers/busybox/) | Minimal BusyBox container for testing |
| [freebsd](manifests/containers/freebsd/) | Native FreeBSD OCI container |
| [nginx](manifests/containers/nginx/) | NGINX container |
| [postgresql](manifests/containers/postgresql/) | PostgreSQL database container |
| [redis](manifests/containers/redis/) | Redis cache server container |

### Core Infrastructure
| Workload | Description |
|----------|-------------|
| [adguard-home](manifests/core-infra/adguard-home/) | Network-wide ads & trackers blocking DNS server |
| [authelia](manifests/core-infra/authelia/) | Authelia - Open-source full-featured authentication server |
| [caddy](manifests/core-infra/caddy/) | Caddy - Powerful web server with automatic HTTPS |
| [haproxy](manifests/core-infra/haproxy/) | HAProxy - Reliable, High Performance TCP/HTTP Load Balancer |
| [headscale](manifests/core-infra/headscale/) | Headscale - Open source self-hosted Tailscale control server |
| [minio](manifests/core-infra/minio/) | MinIO - High Performance S3 compatible Object Storage |
| [sftpgo](manifests/core-infra/sftpgo/) | SFTPGo - Full-featured SFTP server with HTTP/S, FTP/S and WebDAV |
| [simple-jail](manifests/core-infra/simple-jail/) | Minimal FreeBSD jail configuration |
| [unbound](manifests/core-infra/unbound/) | Unbound - Validating, recursive, caching DNS resolver |
| [unifi-controller](manifests/core-infra/unifi-controller/) | UniFi Network Controller - Manage Ubiquiti devices |
| [wireguard](manifests/core-infra/wireguard/) | Fast, modern and secure VPN tunnel |

### Data Science
| Workload | Description |
|----------|-------------|
| [jupyterlab](manifests/data-science/jupyterlab/) | JupyterLab - Next-generation web-based user interface |
| [superset](manifests/data-science/superset/) | Apache Superset - Modern Data Exploration and Visualization Platform |

### Databases
| Workload | Description |
|----------|-------------|
| [clickhouse](manifests/databases/clickhouse/) | ClickHouse - Fast open-source column-oriented DBMS |
| [influxdb](manifests/databases/influxdb/) | InfluxDB - Open source time series database |
| [meilisearch](manifests/databases/meilisearch/) | Meilisearch - search engine written in Rust |
| [postgresql](manifests/databases/postgresql/) | Example PostgreSQL jail with automated initialization |
| [rabbitmq](manifests/databases/rabbitmq/) | RabbitMQ - Widely deployed open source message broker |
| [redis](manifests/databases/redis/) | High-performance Redis cache and message broker |

### Desktops
| Workload | Description |
|----------|-------------|
| [freebsd-desktop](manifests/desktops/freebsd-desktop/) | FreeBSD Desktop with XFCE, XRDP and automated config |
| [guacamole](manifests/desktops/guacamole/) | Apache Guacamole - Clientless remote desktop gateway |
| [xfce](manifests/desktops/xfce/) | XFCE4 desktop environment with VNC access |

### Developer Tools
| Workload | Description |
|----------|-------------|
| [build-agent](manifests/dev-tools/build-agent/) | ARM64 build environment via QEMU user emulation |
| [ci-runner](manifests/dev-tools/ci-runner/) | Self-hosted CI/CD runner with Docker executor |
| [devbox](manifests/dev-tools/devbox/) | Developer workstation with VS Code Server |
| [forgejo](manifests/dev-tools/forgejo/) | Forgejo self-hosted git forge |
| [gitea](manifests/dev-tools/gitea/) | Gitea - Self-hosted Git service |
| [gitlab](manifests/dev-tools/gitlab-linux/) | GitLab Omnibus - Complete DevOps platform |
| [gitlab-runner](manifests/dev-tools/gitlab-runner/) | GitLab Runner - Native FreeBSD runner |
| [openmpi](manifests/dev-tools/openmpi/) | OpenMPI Development Environment |
| [slurm](manifests/dev-tools/slurm/) | SLURM Workload Manager - Controller Node |

### Gaming
| Workload | Description |
|----------|-------------|
| [minecraft](manifests/gaming/minecraft/) | Minecraft PaperMC - High-performance Spigot-compatible server |
| [valheim](manifests/gaming/valheim/) | Valheim Dedicated Server - Co-op survival game |

### Media
| Workload | Description |
|----------|-------------|
| [calibre-web](manifests/media/calibre-web/) | Calibre-Web - Web interface for browsing and reading eBooks |
| [immich](manifests/media/immich/) | Immich - Self-hosted photo and video backup solution |
| [jellyfin](manifests/media/jellyfin/) | Jellyfin media server |
| [komga](manifests/media/komga/) | Komga - Free and open source comics/mangas media server |
| [navidrome](manifests/media/navidrome/) | Navidrome - Modern Music Streaming Server and Library |
| [owncast](manifests/media/owncast/) | Owncast - Self-hosted live video streaming and chat server |
| [photoprism](manifests/media/photoprism/) | PhotoPrism - AI-Powered Photos App |
| [piwigo](manifests/media/piwigo/) | Piwigo - Open source photo gallery software |
| [plex](manifests/media/plex/) | Plex Media Server running in an Ubuntu Linux jail |
| [zenphoto](manifests/media/zenphoto/) | Zenphoto - Simpler web photo gallery |

### Monitoring
| Workload | Description |
|----------|-------------|
| [grafana](manifests/monitoring/grafana/) | Grafana monitoring and observability platform |
| [netdata](manifests/monitoring/netdata/) | Netdata - Real-time performance monitoring |
| [prometheus-grafana](manifests/monitoring/prometheus-grafana/) | Prometheus & Grafana stack |

### Productivity
| Workload | Description |
|----------|-------------|
| [firefly-iii](manifests/productivity/firefly-iii/) | Firefly III - Personal finances manager |
| [kanboard](manifests/productivity/kanboard/) | Kanboard - Kanban project management software |
| [vikunja](manifests/productivity/vikunja/) | Vikunja - The to-do app to organize your life |

### Security
| Workload | Description |
|----------|-------------|
| [cowrie](manifests/security/cowrie-honeypot/) | Cowrie SSH/Telnet Honeypot for attacker observation |
| [kali-linux](manifests/security/kali-linux/) | Advanced Penetration Testing Linux distribution |
| [kali-linux-wifi](manifests/security/kali-linux-wifi/) | Kali Linux with MT7922 WiFi Passthrough |
| [metasploit](manifests/security/metasploit/) | Metasploit Framework - Penetration testing software |
| [openvas](manifests/security/openvas-linux/) | Greenbone Vulnerability Manager (OpenVAS) |
| [vaultwarden](manifests/security/vaultwarden/) | Vaultwarden - Open-source implementation of Bitwarden API |
| [zeek](manifests/security/zeek/) | Zeek - Network security monitoring platform |

### Stacks
| Workload | Description |
|----------|-------------|
| [k3s](manifests/stacks/k3s/) | Lightweight Kubernetes cluster with k3s |
| [nextcloud](manifests/stacks/nextcloud/) | Complete Nextcloud deployment stack |
| [qemu](manifests/stacks/qemu/) | QEMU stack for virtualization |
| [web](manifests/stacks/web/) | Complete 3-tier web application stack |

### Utilities
| Workload | Description |
|----------|-------------|
| [brave](manifests/utilities/brave/) | Brave Browser in an isolated Linux jail |
| [freshrss](manifests/utilities/freshrss/) | FreshRSS - Self-hosted RSS feed aggregator |
| [home-assistant](manifests/utilities/home-assistant/) | Home Assistant Core - Open source home automation |
| [paperless-ngx](manifests/utilities/paperless-ngx/) | Paperless-ngx - Document management system |
| [searxng](manifests/utilities/searxng/) | Optimized SearXNG meta-search engine with nginx and HTTPS |
| [transmission](manifests/utilities/transmission/) | Transmission BitTorrent daemon |

### Virtual Machines
| Workload | Description |
|----------|-------------|
| [alpine](manifests/vms/alpine/) | Lightweight Alpine Linux VM with QEMU |
| [debian-qemu](manifests/vms/debian-qemu/) | Debian 12 Bookworm development VM |
| [freebsd](manifests/vms/freebsd/) | FreeBSD 14.1 server with nuageinit provisioning |
| [freebsd-iso-install](manifests/vms/freebsd-iso-install/) | FreeBSD 14.3 installation from ISO |
| [linux](manifests/vms/linux/) | Ubuntu 24.04 LTS server for development |
| [webserver](manifests/vms/webserver/) | Production web server VM with cloud-init |
| [windows11](manifests/vms/windows11/) | Windows 11 VM Template |

### Web Hosting
| Workload | Description |
|----------|-------------|
| [directus](manifests/web-hosting/directus/) | Directus - Open-Source Data Platform |
| [ghost](manifests/web-hosting/ghost/) | Ghost - Professional publishing platform |
| [nginx](manifests/web-hosting/nginx/) | Production web server running NGINX |
| [nginx-proxy](manifests/web-hosting/nginx-proxy/) | Nginx Proxy Manager with Web UI |
| [strapi](manifests/web-hosting/strapi/) | Strapi - Leading open-source headless CMS |
| [wordpress](manifests/web-hosting/wordpress/) | WordPress stack |

## Testing

To deploy every manifest and run the health check each one declares:

```sh
doas make test-manifests
```

To run just one:

```sh
doas go test -v -run 'TestExampleManifests/nginx' ./test/integration/...
```
