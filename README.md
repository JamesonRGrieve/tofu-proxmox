# terraform-provider-proxmox

A native OpenTofu/Terraform provider for the **Proxmox product family** — Proxmox VE (PVE),
Proxmox Backup Server (PBS), Proxmox Mail Gateway (PMG), and Proxmox Datacenter Manager (PDM) —
generic over their shared `/api2/json` REST API. Two resources cover 100% of the API surface:

- **`proxmox_object`** — a declarative resource addressed by its API `path`. `body` declares only
  the keys you manage; device-returned keys outside `body` are ignored for drift, so a subset
  declaration imports to 0-diff and never clobbers unmanaged fields. Use it for **synchronous**
  config endpoints (e.g. `/nodes/{node}/lxc/{vmid}/config`, `/access/users/{id}`, storage, SDN).
- **`proxmox_task`** — an imperative resource that issues any API call and, when the call returns a
  UPID (a background task), polls `/nodes/{node}/tasks/{upid}/status` to completion. Use it for
  **async** lifecycle ops (create/clone/start/stop/migrate/destroy a VM or CT, backups, etc.).

Sibling of `tofu-aruba-aos` and `tofu-openwrt-ubus`; same house design (generic-over-the-API,
manage-declared-only, import-to-0-diff). Canonical engineering standards:
`/home/jameson/source/ai-prompts/go.md` and `.../tofu.md`.

## Which resource do I use?

| Operation | Resource |
|---|---|
| Set/merge guest, user, storage, SDN, firewall **config** (synchronous PUT) | `proxmox_object` |
| **Create / clone / start / stop / migrate / destroy** a guest; backups; anything returning a UPID | `proxmox_task` |
| Read any path | `proxmox_object` data source |

A POST to `/nodes/{node}/lxc` (create a container) returns a UPID — it is a **task**, not a config
PUT. Putting it through `proxmox_object` would store the UPID string as "body". Use `proxmox_task`.

## Provider configuration

The provider binds to **one product endpoint**; instantiate it per host with `alias` or `for_each`
(mirrors how `net/routers` instantiates `openwrt` per device).

```hcl
provider "proxmox" {
  alias            = "desktop"
  product          = "pve"            # pve | pbs | pmg | pdm (default pve)
  host             = "203.0.113.2"    # port defaults per product: PVE/PMG/PDM 8006, PBS 8007
  api_token_id     = "root@pam!tofu"  # preferred: API token (no CSRF, unattended-friendly)
  api_token_secret = var.pve_token_secret
  insecure         = true             # PVE ships a self-signed cert
}
```

Auth modes: **API token** (preferred — `PVEAPIToken`/`PBSAPIToken`; PBS uses a `:` secret separator)
or **ticket** (`username`+`password` → cookie + `CSRFPreventionToken`). **PMG supports tickets
only** (no API tokens). Secrets come from `TF_VAR_*` / OpenBao at apply — never committed.

## Scope boundary

The provider manages everything reachable through the API. Host-side prerequisites that are **not**
API-settable — the hookscript file under `/var/lib/vz/snippets/`, bind-mount source directories,
NFS mounts, the NVIDIA driver — stay in Ansible / host bootstrap. The provider manages only the
guest config that references them.

## Local development

```bash
make check          # tidy + fmt + vet + test + build (the pre-commit gate)
make install        # build + install to $DEV_BIN_DIR for dev_overrides
git config core.hooksPath .githooks
```

Point OpenTofu at the dev binary:

```hcl
provider_installation {
  dev_overrides { "jamesonrgrieve/proxmox" = "/home/jameson/.local/bin" }
  direct {}
}
```

## License

AGPL-3.0-or-later. Transport uses the Go standard library only — no third-party Proxmox client.
