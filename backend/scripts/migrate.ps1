# PowerShell Migration Helper for Parishram AI

param (
    [Parameter(Mandatory=$false)]
    [ValidateSet("up", "down", "version", "force")]
    [string]$Action = "up",

    [Parameter(Mandatory=$false)]
    [string]$DatabaseUrl = $env:DATABASE_URL,

    [Parameter(Mandatory=$false)]
    [int]$Steps = 1,

    [Parameter(Mandatory=$false)]
    [switch]$ConfirmDown = $false
)

$ErrorActionPreference = "Stop"

# Auto-load DATABASE_URL from .env if present and not provided
if ([string]::IsNullOrWhiteSpace($DatabaseUrl)) {
    $envPath = Join-Path $PSScriptRoot "..\.env"
    if (Test-Path $envPath) {
        $matchedLine = Get-Content $envPath | Where-Object { $_ -match "^DATABASE_URL=(.+)$" } | Select-Object -First 1
        if ($matchedLine) {
            $DatabaseUrl = $matchedLine.Substring("DATABASE_URL=".Length).Trim()
        }
    }
}

# 1. Locate migrate.exe
$migrateExe = "migrate"
if (-not (Get-Command "migrate" -ErrorAction SilentlyContinue)) {
    $fallbackPath = "C:\Users\Lenovo\Downloads\migrate.windows-amd64\migrate.exe"
    if (Test-Path $fallbackPath) {
        $migrateExe = $fallbackPath
    } else {
        Write-Error "golang-migrate executable not found. Please install or add to PATH."
        exit 1
    }
}

# 2. Validate DatabaseUrl argument
if ([string]::IsNullOrWhiteSpace($DatabaseUrl)) {
    Write-Error "DatabaseUrl parameter or DATABASE_URL in .env is required."
    Write-Host "Usage: .\scripts\migrate.ps1 -Action up"
    exit 1
}

if (-not ($DatabaseUrl.StartsWith("postgres://") -or $DatabaseUrl.StartsWith("postgresql://"))) {
    Write-Error "DatabaseUrl must begin with postgres:// or postgresql://"
    exit 1
}

# 3. Guard against accidental down migrations
if ($Action -eq "down" -and -not $ConfirmDown) {
    Write-Warning "Down migration requested. This will drop or alter database tables and may result in DATA LOSS."
    $confirmation = Read-Host "Are you sure you want to rollback $Steps migration step(s)? Type 'YES' to proceed"
    if ($confirmation -ne "YES") {
        Write-Host "Down migration cancelled by user safety check."
        exit 0
    }
}

# In golang-migrate on Windows, relative path with forward slashes is required to avoid drive colon URL parsing bug
$migrationsPath = "db/migrations"

Write-Host "=========================================="
Write-Host "Parishram AI Migration Tool"
Write-Host "Action:      $Action"
Write-Host "Source:      $migrationsPath"
Write-Host "=========================================="

switch ($Action) {
    "up" {
        & $migrateExe -path $migrationsPath -database $DatabaseUrl up
    }
    "down" {
        & $migrateExe -path $migrationsPath -database $DatabaseUrl down $Steps
    }
    "version" {
        & $migrateExe -path $migrationsPath -database $DatabaseUrl version
    }
    "force" {
        & $migrateExe -path $migrationsPath -database $DatabaseUrl force $Steps
    }
}

if ($LASTEXITCODE -ne 0) {
    Write-Error "Migration execution failed with exit code $LASTEXITCODE."
    exit $LASTEXITCODE
}

Write-Host "Migration [$Action] completed successfully."
exit 0
