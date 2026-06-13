# SPDX-License-Identifier: AGPL-3.0-or-later
# proxmox_object — declarative, subset-managed config. Import an existing guest's
# config to 0-diff:  tofu import 'proxmox_object.ct108' '/nodes/desktop/lxc/108/config'

resource "proxmox_object" "ct108" {
  provider      = proxmox.desktop
  path          = "/nodes/desktop/lxc/108/config"
  delete_method = "NONE"
  body = jsonencode({
    cores  = 4
    memory = 16384
    onboot = 1
  })
}

# A user (POST-created collection item): create_path points at the collection.
resource "proxmox_object" "svc_user" {
  provider    = proxmox.desktop
  path        = "/access/users/svc@pve"
  create_path = "/access/users"
  body = jsonencode({
    userid  = "svc@pve"
    comment = "service account"
    enable  = 1
  })
}
