# tofu-proxmox — Agent Guide

A native OpenTofu/Terraform provider for the Proxmox family (PVE/PBS/PMG/PDM), generic over the
shared `/api2/json` REST API. Sibling of `tofu-aruba-aos` / `tofu-openwrt-ubus`; built to the same
house standards.

General Go / Terraform-provider standards are canonical at
`/home/jameson/source/ai-prompts/go.md` (and `.../tofu.md` for the OpenTofu side). Read those first.
This file holds only repo-specific facts.

## Design

- **`proxmox_object`** (declarative): generalization of `arubaos_object`. `path` + declared-JSON
  `body` with `subsetMatches`/`subsetSuppress` → manage-declared-only, import-to-0-diff. `create_path`,
  `delete_method` (DELETE|PUT|NONE), `delete_body`. For **synchronous** config endpoints only.
- **`proxmox_task`** (imperative): generalization of `openwrt_ubus_call`, plus UPID polling. Issues
  `method`+`path`+`params`; if the response is a UPID, `TaskWait` polls the task to terminal status.
  `triggers` re-invoke; optional `destroy_{method,path,params}` runs an inverse op on destroy. For
  **async** lifecycle ops (create/start/migrate/destroy guests, backups).
- **`internal/proxmox`** transport (zero terraform imports): `product.go` (per-product port/cookie/
  token spec), `client.go` (ticket+CSRF and API-token auth, Get/Post/Put/Delete, APIError/NotFound,
  `writeMu`), `task.go` (ParseUPID, TaskStatus, TaskWait).

## Product differences (encoded in `internal/proxmox/product.go`)

| Product | Port | Cookie | API token | Notes |
|---|---|---|---|---|
| pve | 8006 | PVEAuthCookie | `PVEAPIToken=u@r!id=secret` | self-signed cert (insecure=true) |
| pbs | 8007 | PBSAuthCookie | `PBSAPIToken=u@r!id:secret` (colon) | |
| pmg | 8006 | PMGAuthCookie | **none — ticket only** | reject token auth at Configure |
| pdm | 8443 | `__Host-PDMAuthCookie` | `PDMAPIToken=u@r!id:secret` (colon) | verified vs pdm-lab 1.1.1 — NOT the PVE scheme; aggregator/proxy. Token auth works; *ticket* login is cookie-only (PDM returns no body `ticket`), so `client.login()` would need Set-Cookie parsing for password auth — token auth is the supported path. |

## Repo-specific wrinkles

- **Async-everywhere:** PVE writes that matter return UPIDs → use `proxmox_task`, not `proxmox_object`.
- **Key removal:** PVE merges PUT keys; it removes a key via a `delete=` param, not DELETE-on-subpath.
  Unset config keys via `delete_body` (`{"delete":"hookscript,mp0"}`) on update. The subset model
  never unsets keys you stop declaring — by design. Fix spurious diffs in subset logic, never by
  widening stored state.
- **Host-side boundary:** hookscript files, bind-mount source dirs, NFS mounts, NVIDIA drivers are
  **not** API-settable — they live in Ansible/host bootstrap. The provider manages only guest config.
- **ai-lab CT fields** (`hookscript`, `mpN`, `netN` bridge+tag, `unprivileged`, raw `lxc.*`) are all
  settable via `PUT /nodes/{node}/lxc/{vmid}/config`. VMID = `vlan*1000+octet` is config-layer math.

## Relationship to the lab

The `tofu` monorepo currently manages PVE with the third-party `bpg/proxmox` provider in
`opentofu/hv/pve`. Migrating that to this provider is a **separate, later** effort — do not touch
`hv/pve` from here. Service installs on guests are handled by the sibling `tofu-proxmox-services`
provider (ansible orchestration); NetBox remains the source of truth for guest definitions.
