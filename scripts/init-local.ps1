$ErrorActionPreference = 'Stop'
$smtpRoot = Split-Path -Parent $PSScriptRoot
$smtpEnvPath = Join-Path $smtpRoot '.env'
function New-MailSecret {
    $smtpBytes = New-Object byte[] 32
    $smtpRng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    $smtpRng.GetBytes($smtpBytes)
    $smtpRng.Dispose()
    return ($smtpBytes | ForEach-Object { $_.ToString('x2') }) -join ''
}
New-Item -ItemType Directory -Path (Join-Path $smtpRoot 'secrets') -Force | Out-Null
foreach($smtpBackupName in @('backup_passphrase','evidence_passphrase')) {
 $smtpBackupPath=Join-Path $smtpRoot ('secrets\'+$smtpBackupName)
 if (-not (Test-Path -LiteralPath $smtpBackupPath)) {
  [System.IO.File]::WriteAllText($smtpBackupPath,(New-MailSecret),[System.Text.UTF8Encoding]::new($false))
 }
}
if (Test-Path -LiteralPath $smtpEnvPath) { Write-Output 'Existing .env preserved'; exit 0 }
$smtpSettings = Get-Content -LiteralPath (Join-Path $smtpRoot '.env.example') -Raw
foreach ($smtpKey in @('POSTGRES_PASSWORD','APP_DB_PASSWORD','WORKER_DB_PASSWORD','RABBITMQ_PASSWORD','REDIS_PASSWORD')) {
    $smtpSettings = $smtpSettings.Replace("$smtpKey=replace-with-random-value", "$smtpKey=$(New-MailSecret)")
}
$smtpMasterBytes = New-Object byte[] 32
$smtpMasterRng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
$smtpMasterRng.GetBytes($smtpMasterBytes)
$smtpMasterRng.Dispose()
$smtpSettings = $smtpSettings.Replace('MASTER_KEY=replace-with-base64-32-byte-key', 'MASTER_KEY=' + [Convert]::ToBase64String($smtpMasterBytes))
[System.IO.File]::WriteAllText($smtpEnvPath, $smtpSettings, [System.Text.UTF8Encoding]::new($false))
New-Item -ItemType Directory -Path (Join-Path $smtpRoot 'secrets') -Force | Out-Null
Write-Output "Local configuration created: $smtpEnvPath"
