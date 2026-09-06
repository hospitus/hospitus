# Ansible Integration with Hospitus

This directory demonstrates how to integrate Hospitus VMs with existing Ansible playbooks and roles.

## Philosophy

Hospitus uses **cloud-init** for minimal bootstrap configuration:
- Install Python (required by Ansible)
- Configure SSH keys for passwordless access
- Set hostname

All application-level configuration is delegated to **Ansible**, allowing you to reuse existing playbooks and roles without modification.

## Files

| File | Description |
|------|-------------|
| `ansible-ready.toml` | Hospitus manifest for Ansible-ready VM |
| `inventory.ini` | Ansible inventory for Hospitus VMs |
| `site.yml` | Example Ansible playbook |
| `templates/nginx.conf.j2` | Nginx configuration template |

## Quick Start

### 1. Prerequisites

```bash
# Install Ansible on your workstation
pkg install ansible       # FreeBSD
# or
pip install ansible       # Python pip

# Fetch cloud image
hospitus image fetch cloud:ubuntu-24.04
```

### 2. Create the VM

```bash
# Edit ansible-ready.toml to add your SSH public key
vim ansible-ready.toml

# Create and start the VM
hospitus apply --start ansible-ready.toml

# Wait for VM to boot (~2 minutes)
sleep 120
```

### 3. Run Ansible

```bash
# Test connectivity
ansible -i inventory.ini all -m ping

# Run the playbook
ansible-playbook -i inventory.ini site.yml
```

### 4. Verify

```bash
# Check web server
curl http://localhost:8270

# SSH into the VM
ssh -p 2270 ansible@localhost
```

## Using Existing Playbooks

The key insight is that cloud-init only needs to:
1. Install Python
2. Configure SSH access

Everything else uses your existing Ansible infrastructure.

### Example: Using Ansible Galaxy Roles

`requirements.yml`:

```yaml
---
roles:
  - name: geerlingguy.nginx
  - name: geerlingguy.docker
  - name: geerlingguy.postgresql
```

Install them:

```bash
ansible-galaxy install -r requirements.yml
```

Then use one in a playbook:

```yaml
- hosts: webservers
  roles:
    - geerlingguy.nginx
```

### Example: Dynamic Inventory

For multiple VMs, create a dynamic inventory script:

```python
#!/usr/bin/env python3
# hospitus_inventory.py
import subprocess
import json

result = subprocess.run(['hospitus', 'jail', 'list', '-o', 'json'], capture_output=True)
# Parse and output Ansible-compatible inventory
```

## Cloud-init vs Ansible

| Aspect | Cloud-init | Ansible |
|--------|------------|---------|
| When | First boot only | Anytime |
| Idempotent | No | Yes |
| Complexity | Simple | Full configuration management |
| Use case | Bootstrap | Application configuration |

## Tips

### SSH Key Setup

Add your SSH public key to the manifest:

```toml
ssh_authorized_keys = [
    "ssh-ed25519 AAAA... your-key@hostname"
]
```

### Multiple VMs

Create multiple manifests with different SSH ports:

```toml
# web1.toml
[[networks.ports]]
host = 2271
container = 22

# web2.toml
[[networks.ports]]
host = 2272
container = 22
```

Update inventory:

```ini
[webservers]
web1 ansible_host=127.0.0.1 ansible_port=2271
web2 ansible_host=127.0.0.1 ansible_port=2272
```

### FreeBSD VMs

For FreeBSD VMs (using nuageinit), adjust Ansible variables:

```ini
[freebsd]
fbsd-server ansible_host=127.0.0.1 ansible_port=2280 ansible_user=ansible

[freebsd:vars]
ansible_python_interpreter=/usr/local/bin/python3
```

## Integration with CI/CD

Combine with CI/CD runners for automated testing:

1. Hospitus creates test VMs
2. Ansible provisions application
3. Tests run against provisioned infrastructure
4. VMs destroyed after tests

```bash
# In CI pipeline
hospitus apply --start test-vm.toml
sleep 120
ansible-playbook -i inventory.ini site.yml
pytest tests/
hospitus manifest delete -y test-vm.toml
```
