param([int]$DurationSeconds=1900,[string]$Project='selfmail-remediation',[string]$Output='.verification/resources.jsonl')
$ErrorActionPreference='Stop'
$smtpStarted=Get-Date
$smtpOutputPath=[System.IO.Path]::GetFullPath((Join-Path (Get-Location) $Output))
[System.IO.Directory]::CreateDirectory([System.IO.Path]::GetDirectoryName($smtpOutputPath)) | Out-Null
while (((Get-Date)-$smtpStarted).TotalSeconds -lt $DurationSeconds) {
 $smtpNames=@(docker ps --filter "label=com.docker.compose.project=$Project" --format '{{.Names}}')
 if ($smtpNames.Count -gt 0) {
  $smtpRows=@(docker stats --no-stream --format '{{json .}}' @smtpNames)
  foreach ($smtpRow in $smtpRows) {
   $smtpSample=$smtpRow | ConvertFrom-Json
   $smtpSample | Add-Member -NotePropertyName observed_at -NotePropertyValue ([DateTimeOffset]::UtcNow.ToString('o'))
   [System.IO.File]::AppendAllText($smtpOutputPath,($smtpSample | ConvertTo-Json -Compress)+"`n",[System.Text.UTF8Encoding]::new($false))
  }
 }
 Start-Sleep -Seconds 10
}
Write-Output 'Resource sampling complete'
