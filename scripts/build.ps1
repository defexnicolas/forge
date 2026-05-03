# Builds forge with embedded build metadata so the in-app updater can compare
# the running binary against the source repo and pull updates.
#
# Usage:
#   .\scripts\build.ps1               # builds .\forge.exe in repo root
#   .\scripts\build.ps1 -Output bin\forge.exe
#
# What it embeds:
#   Version    = current branch + dirty marker (e.g. "master" or "master-dirty")
#   BuildSHA   = `git rev-parse --short HEAD`
#   BuildTime  = current UTC ISO 8601 timestamp
#   SourceRepo = absolute path to this repo, so the updater knows where to
#                run `git fetch` even when forge is invoked from a different
#                workspace cwd.

[CmdletBinding()]
param(
    [string]$Output = "forge.exe"
)

$ErrorActionPreference = "Stop"

$repo = (Resolve-Path "$PSScriptRoot\..").Path
Push-Location $repo
try {
    $sha = (& git rev-parse --short HEAD).Trim()
    $branch = (& git rev-parse --abbrev-ref HEAD).Trim()
    $dirty = ""
    if ((& git status --porcelain).Trim() -ne "") {
        $dirty = "-dirty"
    }
    $version = "$branch$dirty"
    $buildTime = [DateTime]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ")

    $ldflags = @(
        "-X 'main.Version=$version'",
        "-X 'main.BuildSHA=$sha'",
        "-X 'main.BuildTime=$buildTime'",
        "-X 'main.SourceRepo=$repo'"
    ) -join " "

    Write-Host "Building forge → $Output" -ForegroundColor Cyan
    Write-Host "  version:    $version"
    Write-Host "  commit:     $sha"
    Write-Host "  built:      $buildTime"
    Write-Host "  source:     $repo"

    & go build -ldflags $ldflags -o $Output .\cmd\forge
    if ($LASTEXITCODE -ne 0) {
        throw "go build failed (exit $LASTEXITCODE)"
    }
    Write-Host "Done. Run .\$Output version to verify the embedded metadata." -ForegroundColor Green
}
finally {
    Pop-Location
}
