param([string]$ServiceName = "SimpleWSRelay")

Get-Service -Name $ServiceName -ErrorAction SilentlyContinue | Format-List Name, DisplayName, Status, StartType
