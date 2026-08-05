# build.ps1 — builds lid.exe for distribution
# Run once from the project root:  .\build.ps1
#
# Requirements: Go 1.22+  (https://go.dev/dl/)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

Write-Host "==> Downloading / verifying dependencies..."
go mod tidy

Write-Host "==> Installing rsrc (embeds the Windows manifest)..."
go install github.com/akavel/rsrc@latest

Write-Host "==> Generating rsrc.syso from main.manifest..."
rsrc -manifest main.manifest -o rsrc.syso

Write-Host "==> Building lid.exe (no console window)..."
go build -ldflags="-H windowsgui -s -w" -o lid.exe .

Write-Host ""
Write-Host "Build complete -> lid.exe"
Write-Host "Distribute lid.exe as a standalone file — no runtime required."
