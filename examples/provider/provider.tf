# SPDX-License-Identifier: AGPL-3.0-or-later
# Example: the four Proxmox products, and an ai-lab GPU CT on PVE.

terraform {
  required_providers {
    proxmox = { source = "jamesonrgrieve/proxmox" }
  }
}

variable "pve_token_secret" {
  type      = string
  sensitive = true
} # from OpenBao -> TF_VAR_pve_token_secret

# One aliased instance per product/host (use for_each over an inventory map to
# scale, mirroring net/routers' per-device provider instantiation).
provider "proxmox" {
  alias            = "desktop"
  product          = "pve" # default; shown for clarity
  host             = "203.0.113.2"
  api_token_id     = "root@pam!tofu"
  api_token_secret = var.pve_token_secret
  insecure         = true
}

provider "proxmox" {
  alias            = "backup"
  product          = "pbs" # port defaults to 8007; token uses a ':' secret separator
  host             = "203.0.113.20"
  api_token_id     = "root@pam!tofu"
  api_token_secret = var.pve_token_secret
  insecure         = true
}

provider "proxmox" {
  alias    = "mail"
  product  = "pmg" # ticket-only — no API token
  host     = "203.0.113.30"
  username = "root@pam"
  password = var.pve_token_secret # reuse var for the example
  insecure = true
}

# ---------------------------------------------------------------------------
# ai-lab GPU container (desktop CT 108 "sglang"), VMID = vlan*1000 + octet.
# ---------------------------------------------------------------------------
locals {
  node  = "desktop"
  vmid  = 108
  same  = "203.0.113.8/25"  # vmbr3 same-hypervisor
  cross = "198.51.100.8/27" # vmbr1 cross-hypervisor (tag 5)
}

# 1) Create the CT (async — returns a UPID that is polled to completion).
resource "proxmox_task" "ct108_create" {
  provider = proxmox.desktop
  method   = "POST"
  path     = "/nodes/${local.node}/lxc"
  params = jsonencode({
    vmid         = local.vmid
    hostname     = "sglang"
    ostemplate   = "local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst"
    storage      = "pve-zfs"
    rootfs       = "pve-zfs:16"
    cores        = 4
    memory       = 16384
    swap         = 4096
    unprivileged = 0
    features     = "nesting=1"
    onboot       = 1
    searchdomain = "local"
    hookscript   = "local:snippets/gpu-passthrough.sh"
    net0         = "name=eth1,bridge=vmbr3,gw=203.0.113.1,ip=${local.same},type=veth"
    net1         = "name=eth0,bridge=vmbr1,ip=${local.cross},tag=5,type=veth"
  })
  await           = true
  timeout_seconds = 600

  # On destroy: purge the CT.
  destroy_method = "DELETE"
  destroy_path   = "/nodes/${local.node}/lxc/${local.vmid}"
  destroy_params = jsonencode({ purge = 1, force = 1 })
}

# 2) Manage API-settable config keys declaratively (subset -> 0-diff). To unset a
#    key later, use delete_method=PUT + delete_body={"delete":"mp0"}.
resource "proxmox_object" "ct108_config" {
  provider      = proxmox.desktop
  path          = "/nodes/${local.node}/lxc/${local.vmid}/config"
  delete_method = "NONE" # lifecycle owned by proxmox_task above
  body = jsonencode({
    hookscript = "local:snippets/gpu-passthrough.sh"
    mp0        = "/srv/models/sglang,mp=/root/.cache/huggingface"
  })
  depends_on = [proxmox_task.ct108_create]
}

# 3) Start the CT; re-runs if the config changes.
resource "proxmox_task" "ct108_start" {
  provider       = proxmox.desktop
  method         = "POST"
  path           = "/nodes/${local.node}/lxc/${local.vmid}/status/start"
  triggers       = { config = proxmox_object.ct108_config.id }
  destroy_method = "POST"
  destroy_path   = "/nodes/${local.node}/lxc/${local.vmid}/status/stop"
  depends_on     = [proxmox_object.ct108_config]
}

# NOTE: GPU device passthrough (raw lxc.cgroup2.devices.allow / lxc.mount.entry
# lines, the snippets/gpu-passthrough.sh file itself, the NFS bind source, the
# NVIDIA driver) is NOT API-settable — it is applied host-side by Ansible / the
# hookscript. This provider manages only the guest config that references it.
