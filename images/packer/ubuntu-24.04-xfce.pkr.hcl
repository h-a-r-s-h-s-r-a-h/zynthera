# Golden image: Ubuntu 24.04 LTS with the XFCE desktop (spec section 13).
#
# Starts from Ubuntu's official cloud image, boots it once under KVM, installs
# the desktop and the Zynthera pieces, then seals it so every clone is unique.
# Build it with images/build.sh, not by hand.

packer {
  required_plugins {
    qemu = {
      version = ">= 1.1.0"
      source  = "github.com/hashicorp/qemu"
    }
  }
}

variable "output_dir" {
  type        = string
  description = "Directory Packer writes the raw build result to. Must not exist yet."
}

variable "bridge_binary" {
  type        = string
  description = "Path to the statically built zynthera-file-bridge."
}

variable "apt_mirror" {
  type        = string
  default     = ""
  description = "Ubuntu mirror to use during the build only, e.g. http://in.archive.ubuntu.com/ubuntu. Empty means the default archive. The finished image always points at the default archive."
}

variable "cloud_image_url" {
  type    = string
  default = "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img"
}

variable "cloud_image_checksum" {
  type    = string
  default = "file:https://cloud-images.ubuntu.com/noble/current/SHA256SUMS"
}

source "qemu" "ubuntu" {
  iso_url      = var.cloud_image_url
  iso_checksum = var.cloud_image_checksum
  disk_image   = true

  # Small virtual size: clones can grow, but never shrink.
  disk_size      = "16G"
  format         = "qcow2"
  disk_interface = "virtio"
  net_device     = "virtio-net"

  accelerator = "kvm"
  headless    = true
  cpus        = 4
  memory      = 4096
  qemuargs    = [["-cpu", "host"]]

  # Build-time cloud-init: creates the temporary build user Packer logs in as.
  cd_files = ["${path.root}/build-seed/user-data", "${path.root}/build-seed/meta-data"]
  cd_label = "cidata"

  ssh_username = "zynbuild"
  ssh_password = "zynbuild"
  ssh_timeout  = "15m"

  # Removes the build user, clears everything unique and powers off.
  shutdown_command = "sudo /usr/local/sbin/zyn-seal"
  shutdown_timeout = "10m"

  output_directory = var.output_dir
  vm_name          = "build.qcow2"
}

build {
  sources = ["source.qemu.ubuntu"]

  provisioner "shell" {
    inline = ["cloud-init status --wait || true"]
  }

  provisioner "file" {
    source      = var.bridge_binary
    destination = "/tmp/zynthera-file-bridge"
  }

  provisioner "file" {
    source      = "${path.root}/../files/zynthera-file-bridge.service"
    destination = "/tmp/zynthera-file-bridge.service"
  }

  provisioner "file" {
    source      = "${path.root}/../scripts/seal.sh"
    destination = "/tmp/zyn-seal"
  }

  provisioner "shell" {
    execute_command   = "{{ .Vars }} sudo -E bash -euo pipefail '{{ .Path }}'"
    environment_vars  = ["ZYN_APT_MIRROR=${var.apt_mirror}"]
    expect_disconnect = false
    scripts = [
      "${path.root}/../scripts/01-base.sh",
      "${path.root}/../scripts/02-desktop.sh",
      "${path.root}/../scripts/03-zynthera.sh",
    ]
  }
}
