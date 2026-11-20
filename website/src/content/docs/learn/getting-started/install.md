---
title: Install the CLI and the editor
description: "Install the naut command-line tool on macOS, Linux or Windows, and the VS Code extension, and check both work."
sidebar:
  order: 2
---

This lesson gets two things onto your computer: the `naut` command-line tool
and, if you want it, the VS Code extension. You need `naut` for everything
that follows. About five minutes.

**Platforms:** nautilus is one static binary with no runtime dependencies.
Every release ships builds for **macOS** (Apple Silicon and Intel),
**Linux** (x86-64 and arm64), and **Windows** (x86-64 and arm64); the
controller it builds runs anywhere the same binary does, from a laptop to a
Raspberry Pi to a Kubernetes pod. VS Code is the editor experience on all
three. The
[release page](https://github.com/joyautomation/nautilus/releases/latest)
has the archives and a `checksums.txt`.


## Install the CLI

### macOS — pick `arm64` for Apple Silicon, `amd64` for Intel:

```sh
v=$(curl -fsSL https://api.github.com/repos/joyautomation/nautilus/releases/latest | grep '"tag_name"' | cut -d'"' -f4)
curl -fsSL -o naut.tar.gz "https://github.com/joyautomation/nautilus/releases/download/$v/nautilus_${v#v}_darwin_arm64.tar.gz"
tar xzf naut.tar.gz naut && rm naut.tar.gz
sudo mv naut /usr/local/bin/
```

Downloading with `curl` skips Gatekeeper's quarantine. If you fetched the
archive in a browser instead and macOS refuses to open the binary, clear the
flag once: `xattr -d com.apple.quarantine /usr/local/bin/naut`.

### Linux — `amd64` or `arm64`:

```sh
v=$(curl -fsSL https://api.github.com/repos/joyautomation/nautilus/releases/latest | grep '"tag_name"' | cut -d'"' -f4)
curl -fsSL -o naut.tar.gz "https://github.com/joyautomation/nautilus/releases/download/$v/nautilus_${v#v}_linux_amd64.tar.gz"
tar xzf naut.tar.gz naut && rm naut.tar.gz
sudo install naut /usr/local/bin/naut
```

### Windows — PowerShell, `amd64` or `arm64`:

```powershell
$v = (Invoke-RestMethod https://api.github.com/repos/joyautomation/nautilus/releases/latest).tag_name
Invoke-WebRequest "https://github.com/joyautomation/nautilus/releases/download/$v/nautilus_$($v.TrimStart('v'))_windows_amd64.zip" -OutFile nautilus.zip
Expand-Archive nautilus.zip -DestinationPath "$env:LOCALAPPDATA\nautilus" -Force
[Environment]::SetEnvironmentVariable("Path", "$env:LOCALAPPDATA\nautilus;" + [Environment]::GetEnvironmentVariable("Path", "User"), "User")
```

Open a new terminal afterwards so the `Path` change is picked up.

### Any OS, with Go 1.25+ installed

```sh
go install github.com/joyautomation/nautilus/cmd/naut@latest
```

This puts the binary in `$(go env GOPATH)/bin`, which needs to be on your
`PATH`. Whichever route you took, `naut version` should now answer.

## Check it works

```sh
naut version
```

It prints the version you installed, for example `nautilus 0.22.0`. If your
terminal says "command not found", open a **new** terminal (the install
changed your `PATH`) and try again; the
[tutorial](/tutorial/#naut-command-not-found) lists the other causes.

## The VS Code extension

Install **nautilus IEC 61131-3** from the
[VS Code Marketplace](https://marketplace.visualstudio.com/items?itemName=joyauto.vscode-iec)
or [Open VSX](https://open-vsx.org/extension/joyauto/vscode-iec). (*Switch
to Pre-Release Version* on the extension page gets builds from `main`.) With
your project open and the controller running you get compile
diagnostics as you type, go-to-definition, find all references, hover,
completion, and live tag values next to identifiers in your program.

On macOS, VS Code launched from the Dock or Spotlight gets the login `PATH`,
not your shell's, so it may not find `naut` even though your terminal
does. If the extension reports it could not start the language server, run
*nautilus: Install or Update the naut CLI*, set `nautilus.cliPath` to the
full path (`which naut`), or launch VS Code from a terminal with `code .`.

## Next

[Your first controller, running](/learn/getting-started/first-controller/):
make a project and watch it scan.
