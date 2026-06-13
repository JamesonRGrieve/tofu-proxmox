# SPDX-License-Identifier: AGPL-3.0-or-later
# proxmox_task — imperative async operations (anything returning a UPID).

# Clone a VM template into a new VM, await the task, and destroy on teardown.
resource "proxmox_task" "clone_web" {
  provider = proxmox.desktop
  method   = "POST"
  path     = "/nodes/desktop/qemu/9000/clone"
  params = jsonencode({
    newid = 150
    name  = "web-01"
    full  = 1
  })
  await           = true
  timeout_seconds = 1200

  destroy_method = "DELETE"
  destroy_path   = "/nodes/desktop/qemu/150"
  destroy_params = jsonencode({ purge = 1 })
}

# Read a task's status by UPID.
data "proxmox_task" "clone_status" {
  provider = proxmox.desktop
  upid     = proxmox_task.clone_web.upid
}
