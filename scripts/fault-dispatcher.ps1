param([string]$Container='selfmail-remediation-dispatcher-1')
$ErrorActionPreference='Stop'
$smtpOwner=docker inspect $Container --format '{{index .Config.Labels "com.docker.compose.project"}}'
if($smtpOwner -ne 'selfmail-remediation'){throw 'Faults are restricted to the isolated remediation stack'}
try {
 docker pause $Container
 if($LASTEXITCODE -ne 0){throw 'Pause failed'}
 for($smtpStep=0;$smtpStep -lt 11;$smtpStep++){Start-Sleep -Seconds 10}
} finally {docker unpause $Container}
