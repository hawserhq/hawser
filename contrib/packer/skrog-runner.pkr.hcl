# Bake a Windows runner image with Skrog installed and the engine pre-imported
# (#143). A freshly provisioned runner boots ready: no downloads, no first-run
# import. The shape is the same for any builder — swap the `source` block for
# Hyper-V, vSphere, or QEMU; the provisioner steps are what matter.
#
#   packer init  .
#   packer build -var skrog_version=0.3.0 -var bundle_path=./skrog-29.7.2.zip .
#
# Auto-logon (the runner needs an interactive session — WSL2 cannot start from
# a service) is deliberately NOT configured here: it writes a credential into
# the image. Do it in your own provisioner with Sysinternals Autologon, per
# docs/auto-logon-runner.md, and verify with `skrog runner check`.

packer {
  required_plugins {
    azure = {
      source  = "github.com/hashicorp/azure"
      version = ">= 2.0.0"
    }
  }
}

variable "skrog_version" {
  type        = string
  description = "Skrog release to install, e.g. 0.3.0 (pinned: never 'latest' in an image)."
}

variable "bundle_path" {
  type        = string
  default     = ""
  description = "Optional air-gap bundle from `skrog bundle` (#75). When set, the engine imports offline; otherwise it downloads the pinned rootfs."
}

variable "lockfile_path" {
  type        = string
  default     = ""
  description = "Optional skrog.lock to pin the engine (install --locked)."
}

variable "setup_skrog_ref" {
  type        = string
  default     = "v1"
  description = "Tag of zcsizmadia/setup-skrog whose install script is used (one source of truth with the GitHub Action)."
}

variable "runner_user" {
  type        = string
  default     = "skrog-runner"
  description = "Local account the runner will auto-log on as; Skrog is installed for this user."
}

# --- Azure: Windows Server 2022 Datacenter with nested virtualization (WSL2
# needs it: pick a v3/v4/v5 size that supports it). Other builders: keep the
# provisioners, replace this block.
source "azure-arm" "runner" {
  use_azure_cli_auth                = true
  location                          = "eastus"
  vm_size                           = "Standard_D4s_v5"
  os_type                           = "Windows"
  image_publisher                   = "MicrosoftWindowsServer"
  image_offer                       = "WindowsServer"
  image_sku                         = "2022-datacenter-azure-edition"
  managed_image_name                = "skrog-runner-${var.skrog_version}-{{timestamp}}"
  managed_image_resource_group_name = "rg-skrog-images"

  communicator   = "winrm"
  winrm_use_ssl  = true
  winrm_insecure = true
  winrm_timeout  = "10m"
  winrm_username = "packer"
}

build {
  name    = "skrog-runner"
  sources = ["source.azure-arm.runner"]

  # 1. WSL2 itself. Needs a reboot before a distro can be imported.
  provisioner "powershell" {
    inline = [
      "wsl --install --no-distribution",
      "wsl --update",
    ]
    valid_exit_codes = [0, 1]
  }
  provisioner "windows-restart" {}

  # 2. The bundle (optional) and the lockfile (optional) ride along.
  provisioner "file" {
    source      = var.bundle_path == "" ? "scripts/provision-skrog.ps1" : var.bundle_path
    destination = var.bundle_path == "" ? "C:\\Windows\\Temp\\ignore.ps1" : "C:\\Windows\\Temp\\skrog-bundle.zip"
  }
  provisioner "file" {
    source      = "scripts/provision-skrog.ps1"
    destination = "C:\\Windows\\Temp\\provision-skrog.ps1"
  }

  # 3. Install Skrog (verified download via the setup-skrog script), import
  #    the engine, register autostart, gate the image on `skrog doctor`.
  provisioner "powershell" {
    environment_vars = [
      "SKROG_VERSION=${var.skrog_version}",
      "SKROG_BUNDLE=${var.bundle_path == "" ? "" : "C:\\Windows\\Temp\\skrog-bundle.zip"}",
      "SKROG_LOCKFILE=${var.lockfile_path}",
      "SETUP_SKROG_REF=${var.setup_skrog_ref}",
      "SKROG_RUNNER_USER=${var.runner_user}",
    ]
    inline = ["pwsh -NoProfile -File C:\\Windows\\Temp\\provision-skrog.ps1"]
  }

  # 4. Generalize for Azure. Other builders: sysprep as they require.
  provisioner "powershell" {
    inline = [
      "& $env:SystemRoot\\System32\\Sysprep\\Sysprep.exe /oobe /generalize /quiet /quit /mode:vm",
      "while ($true) { $s = (Get-ItemProperty HKLM:\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Setup\\State).ImageState; if ($s -eq 'IMAGE_STATE_GENERALIZE_RESEAL_TO_OOBE') { break }; Start-Sleep 10 }",
    ]
  }
}
