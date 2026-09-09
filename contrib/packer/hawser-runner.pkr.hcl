# Bake a Windows runner image with Hawser installed and the engine pre-imported
# (#143). A freshly provisioned runner boots ready: no downloads, no first-run
# import. The shape is the same for any builder — swap the `source` block for
# Hyper-V, vSphere, or QEMU; the provisioner steps are what matter.
#
#   packer init  .
#   packer build -var hawser_version=0.3.0 -var bundle_path=./hawser-29.7.2.zip .
#
# Auto-logon (the runner needs an interactive session — WSL2 cannot start from
# a service) is deliberately NOT configured here: it writes a credential into
# the image. Do it in your own provisioner with Sysinternals Autologon, per
# docs/auto-logon-runner.md, and verify with `hawser runner check`.

packer {
  required_plugins {
    azure = {
      source  = "github.com/hashicorp/azure"
      version = ">= 2.0.0"
    }
  }
}

variable "hawser_version" {
  type        = string
  description = "Hawser release to install, e.g. 0.3.0 (pinned: never 'latest' in an image)."
}

variable "bundle_path" {
  type        = string
  default     = ""
  description = "Optional air-gap bundle from `hawser bundle` (#75). When set, the engine imports offline; otherwise it downloads the pinned rootfs."
}

variable "lockfile_path" {
  type        = string
  default     = ""
  description = "Optional hawser.lock to pin the engine (install --locked)."
}

variable "setup_hawser_ref" {
  type        = string
  default     = "v1"
  description = "Tag of zcsizmadia/setup-hawser whose install script is used (one source of truth with the GitHub Action)."
}

variable "runner_user" {
  type        = string
  default     = "hawser-runner"
  description = "Local account the runner will auto-log on as; Hawser is installed for this user."
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
  managed_image_name                = "hawser-runner-${var.hawser_version}-{{timestamp}}"
  managed_image_resource_group_name = "rg-hawser-images"

  communicator   = "winrm"
  winrm_use_ssl  = true
  winrm_insecure = true
  winrm_timeout  = "10m"
  winrm_username = "packer"
}

build {
  name    = "hawser-runner"
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
    source      = var.bundle_path == "" ? "scripts/provision-hawser.ps1" : var.bundle_path
    destination = var.bundle_path == "" ? "C:\\Windows\\Temp\\ignore.ps1" : "C:\\Windows\\Temp\\hawser-bundle.zip"
  }
  provisioner "file" {
    source      = "scripts/provision-hawser.ps1"
    destination = "C:\\Windows\\Temp\\provision-hawser.ps1"
  }

  # 3. Install Hawser (verified download via the setup-hawser script), import
  #    the engine, register autostart, gate the image on `hawser doctor`.
  provisioner "powershell" {
    environment_vars = [
      "HAWSER_VERSION=${var.hawser_version}",
      "HAWSER_BUNDLE=${var.bundle_path == "" ? "" : "C:\\Windows\\Temp\\hawser-bundle.zip"}",
      "HAWSER_LOCKFILE=${var.lockfile_path}",
      "SETUP_HAWSER_REF=${var.setup_hawser_ref}",
      "HAWSER_RUNNER_USER=${var.runner_user}",
    ]
    inline = ["pwsh -NoProfile -File C:\\Windows\\Temp\\provision-hawser.ps1"]
  }

  # 4. Generalize for Azure. Other builders: sysprep as they require.
  provisioner "powershell" {
    inline = [
      "& $env:SystemRoot\\System32\\Sysprep\\Sysprep.exe /oobe /generalize /quiet /quit /mode:vm",
      "while ($true) { $s = (Get-ItemProperty HKLM:\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Setup\\State).ImageState; if ($s -eq 'IMAGE_STATE_GENERALIZE_RESEAL_TO_OOBE') { break }; Start-Sleep 10 }",
    ]
  }
}
