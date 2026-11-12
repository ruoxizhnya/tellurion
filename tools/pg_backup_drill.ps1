# 备份/恢复演练：整库 pg_dump -Fc → 独立 _test 库 pg_restore → 结构计数与逐表行数对拍。
# 只操作本机实例；不触碰除 -Database 与 -TestDatabase 之外的任何对象。
# 凭据只从环境变量 PGPASSWORD 读取，不写入任何文件。
param(
    [string]$Database = 'tellurion',
    [string]$TestDatabase = 'tellurion_dr_test',
    [string]$DumpPath = (Join-Path $env:TEMP ("{0}_dr_{1:yyyyMMddHHmmss}.dump" -f $Database, (Get-Date))),
    [string]$PgBin = 'C:\Program Files\PostgreSQL\16\bin',
    [string]$HostName = '127.0.0.1',
    [int]$Port = 5432,
    [string]$User = 'postgres',
    [switch]$KeepTestDatabase
)

$ErrorActionPreference = 'Stop'
if (-not $env:PGPASSWORD) { Write-Error 'PGPASSWORD 未设置：脚本不会读取任何内置口令'; exit 2 }
if ($Database -eq $TestDatabase) { Write-Error 'TestDatabase 不能与源库同名'; exit 2 }

$psql = Join-Path $PgBin 'psql.exe'
$pgDump = Join-Path $PgBin 'pg_dump.exe'
$pgRestore = Join-Path $PgBin 'pg_restore.exe'
foreach ($exe in @($psql, $pgDump, $pgRestore)) {
    if (-not (Test-Path $exe)) { Write-Error "找不到 $exe"; exit 2 }
}

$common = @('-h', $HostName, '-p', "$Port", '-U', $User)
$plain = [System.IO.Path]::ChangeExtension($DumpPath, '.sql')

function Invoke-Psql([string]$db, [string]$sql) {
    $out = & $psql @common -d $db -At -c $sql
    if ($LASTEXITCODE -ne 0) { throw "psql 失败 (db=$db): $sql" }
    return $out
}

# 1) 备份
& $pgDump @common -d $Database -Fc -f $DumpPath
if ($LASTEXITCODE -ne 0) { Write-Error "pg_dump 失败 EXIT=$LASTEXITCODE"; exit 1 }
Write-Output "DUMP_OK path=$DumpPath size=$((Get-Item $DumpPath).Length)"

# 2) 转成纯文本，用于取得“备份当时”的逐表行数（隔离演练期间的线上写入）
& $pgRestore -a -f $plain $DumpPath
if ($LASTEXITCODE -ne 0) { Write-Error "pg_restore -f 失败 EXIT=$LASTEXITCODE"; exit 1 }
$dumpRows = @{}
$current = $null
foreach ($line in Get-Content $plain) {
    if ($line -match '^COPY (.+?) \(') { $current = $matches[1]; $dumpRows[$current] = 0 }
    elseif ($line -eq '\.') { $current = $null }
    elseif ($current) { $dumpRows[$current]++ }
}
Write-Output "DUMP_TABLES=$($dumpRows.Count)"

# 3) 恢复到独立 _test 库
Invoke-Psql 'postgres' "DROP DATABASE IF EXISTS $TestDatabase" | Out-Null
Invoke-Psql 'postgres' "CREATE DATABASE $TestDatabase" | Out-Null
& $pgRestore @common -d $TestDatabase --no-owner --no-privileges --exit-on-error $DumpPath
if ($LASTEXITCODE -ne 0) { Write-Error "pg_restore 失败 EXIT=$LASTEXITCODE"; exit 1 }

# 4) 结构计数对拍
$structSql = "select 'schemas='||(select count(*) from pg_namespace where nspname not like 'pg_%' and nspname<>'information_schema')||' tables='||(select count(*) from pg_tables where schemaname not like 'pg_%' and schemaname<>'information_schema')||' indexes='||(select count(*) from pg_indexes where schemaname not like 'pg_%' and schemaname<>'information_schema')||' sequences='||(select count(*) from pg_class c join pg_namespace n on n.oid=c.relnamespace where c.relkind='S' and n.nspname not like 'pg_%')||' constraints='||(select count(*) from pg_constraint c join pg_namespace n on n.oid=c.connamespace where n.nspname not like 'pg_%')"
$srcStruct = (Invoke-Psql $Database $structSql).Trim()
$dstStruct = (Invoke-Psql $TestDatabase $structSql).Trim()
if ($srcStruct -ne $dstStruct) {
    Write-Output "STRUCTURE_MISMATCH src=$srcStruct dst=$dstStruct"
    exit 1
}
Write-Output "STRUCTURE_MATCH $srcStruct"

# 5) 逐表行数对拍（基准 = 备份当时的 COPY 行数，不是线上实时数）
$countSql = "select schemaname||'.'||tablename||'='||(xpath('/row/c/text()', query_to_xml(format('select count(*) as c from %I.%I', schemaname, tablename), false, true, '')))[1]::text::bigint from pg_tables where schemaname not like 'pg_%' and schemaname<>'information_schema' order by 1"
$dstRows = @{}
foreach ($row in (Invoke-Psql $TestDatabase $countSql)) {
    $kv = $row.Split('=', 2)
    $dstRows[$kv[0]] = [int]$kv[1]
}
$mismatch = @()
foreach ($table in $dumpRows.Keys) {
    $actual = if ($dstRows.ContainsKey($table)) { $dstRows[$table] } else { -1 }
    if ($actual -ne $dumpRows[$table]) { $mismatch += "$table dump=$($dumpRows[$table]) restored=$actual" }
}
if ($mismatch.Count -gt 0) {
    Write-Output "ROWCOUNTS_MISMATCH count=$($mismatch.Count)"
    $mismatch | ForEach-Object { Write-Output "  $_" }
    exit 1
}
Write-Output "ROWCOUNTS_MATCH dump_tables=$($dumpRows.Count) restored_tables=$($dstRows.Count)"

# 6) 清理（默认删演练库与中间文件；-KeepTestDatabase 时保留 _test 库供人工核查）
Remove-Item $plain -Force
if (-not $KeepTestDatabase) {
    Invoke-Psql 'postgres' "DROP DATABASE IF EXISTS $TestDatabase" | Out-Null
    Write-Output "CLEANUP test_db_dropped=$TestDatabase"
} else {
    Write-Output "KEEP test_db=$TestDatabase dump=$DumpPath"
}
exit 0
