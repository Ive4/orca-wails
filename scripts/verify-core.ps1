#Requires -Version 7.2
<#
.SYNOPSIS
Qualify the extracted core using its own pinned Node under a non-breakaway Job.
.DESCRIPTION
Checks the plain-Node module graph, a real ConPTY spawn, readiness identity,
terminal-daemon health, browser assets and Job process-tree cleanup. It never
prints or publishes readiness output because that output contains pairing secrets.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$CoreDirectory,
    [Parameter(Mandatory)][string]$ReportPath,
    [ValidateRange(15, 300)][int]$StartupTimeoutSeconds = 120
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if (-not $IsWindows) { throw 'This qualification gate requires a Windows runner.' }
$CoreDirectory = (Resolve-Path -LiteralPath $CoreDirectory).Path
$manifest = Get-Content -LiteralPath (Join-Path $CoreDirectory 'core-manifest.json') -Raw | ConvertFrom-Json -AsHashtable
if ($manifest.schema_version -ne 1 -or $manifest.target -ne 'x86_64-pc-windows-msvc') { throw 'Unexpected manifest target/schema.' }
$nodeExe = Join-Path $CoreDirectory $manifest.executable
$slot = Join-Path $CoreDirectory 'orcad'
$entry = Join-Path $slot 'orcad-server.js'
$actualNodeHash = (Get-FileHash -LiteralPath $nodeExe -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actualNodeHash -cne $manifest.node_sha256) { throw 'Node executable differs from the manifest.' }
if (Test-Path -LiteralPath (Join-Path $CoreDirectory 'resources/app.asar')) { throw 'Electron resources unexpectedly entered the core package.' }

if (-not ('OrcaCoreSmoke.NativeJob' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Text;
using System.Threading;
namespace OrcaCoreSmoke {
    public sealed class NativeJob : IDisposable {
        [StructLayout(LayoutKind.Sequential)] struct SECURITY_ATTRIBUTES {
            public int nLength; public IntPtr securityDescriptor; [MarshalAs(UnmanagedType.Bool)] public bool inherit;
        }
        [StructLayout(LayoutKind.Sequential)] struct BASIC_LIMIT {
            public long perProcessTime, perJobTime; public uint flags;
            public UIntPtr minWorkingSet, maxWorkingSet; public uint activeLimit;
            public UIntPtr affinity; public uint priorityClass, schedulingClass;
        }
        [StructLayout(LayoutKind.Sequential)] struct IO_COUNTERS {
            public ulong readOps, writeOps, otherOps, readBytes, writeBytes, otherBytes;
        }
        [StructLayout(LayoutKind.Sequential)] struct EXTENDED_LIMIT {
            public BASIC_LIMIT basic; public IO_COUNTERS io;
            public UIntPtr processMemory, jobMemory, peakProcessMemory, peakJobMemory;
        }
        [StructLayout(LayoutKind.Sequential)] struct ACCOUNTING {
            public long user, kernel, periodUser, periodKernel;
            public uint faults, totalProcesses, activeProcesses, terminatedProcesses;
        }
        [StructLayout(LayoutKind.Sequential, CharSet=CharSet.Unicode)] struct STARTUPINFO {
            public int cb; public string reserved, desktop, title;
            public uint x,y,xSize,ySize,xChars,yChars,fill,flags;
            public short show,reservedSize; public IntPtr reserved2,stdin,stdout,stderr;
        }
        [StructLayout(LayoutKind.Sequential)] struct STARTUPINFOEX { public STARTUPINFO info; public IntPtr attributes; }
        [StructLayout(LayoutKind.Sequential)] struct PROCESS_INFO { public IntPtr process,thread; public uint pid,tid; }
        [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern IntPtr CreateJobObjectW(IntPtr a,string n);
        [DllImport("kernel32.dll", SetLastError=true)] static extern bool SetInformationJobObject(IntPtr j,int c,ref EXTENDED_LIMIT i,uint n);
        [DllImport("kernel32.dll", SetLastError=true)] static extern bool QueryInformationJobObject(IntPtr j,int c,out ACCOUNTING i,uint n,IntPtr r);
        [DllImport("kernel32.dll", SetLastError=true)] static extern bool TerminateJobObject(IntPtr j,uint c);
        [DllImport("kernel32.dll", SetLastError=true)] static extern bool InitializeProcThreadAttributeList(IntPtr p,int count,uint flags,ref IntPtr size);
        [DllImport("kernel32.dll", SetLastError=true)] static extern bool UpdateProcThreadAttribute(IntPtr p,uint f,IntPtr a,IntPtr v,IntPtr size,IntPtr previous,IntPtr ret);
        [DllImport("kernel32.dll")] static extern void DeleteProcThreadAttributeList(IntPtr p);
        [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern IntPtr CreateFileW(string p,uint a,uint s,ref SECURITY_ATTRIBUTES sa,uint creation,uint flags,IntPtr template);
        [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern bool CreateProcessW(string app,StringBuilder command,IntPtr pa,IntPtr ta,bool inherit,uint flags,IntPtr env,string cwd,ref STARTUPINFOEX startup,out PROCESS_INFO p);
        [DllImport("kernel32.dll", SetLastError=true)] static extern uint ResumeThread(IntPtr t);
        [DllImport("kernel32.dll", SetLastError=true)] static extern uint WaitForSingleObject(IntPtr h,uint ms);
        [DllImport("kernel32.dll", SetLastError=true)] static extern bool GetExitCodeProcess(IntPtr p,out uint code);
        [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr h);
        IntPtr job,process;
        public uint Pid { get; private set; }
        static void Check(bool value) { if (!value) throw new Win32Exception(Marshal.GetLastWin32Error()); }
        static string Quote(string value) {
            var b = new StringBuilder("\""); int slash=0;
            foreach(char c in value) {
                if(c=='\\') { slash++; continue; }
                if(c=='\"') { b.Append('\\',slash*2+1); b.Append(c); slash=0; continue; }
                b.Append('\\',slash); slash=0; b.Append(c);
            }
            b.Append('\\',slash*2); return b.Append('"').ToString();
        }
        public NativeJob(string exe,string[] args,string cwd,string stdout,string stderr) {
            job=CreateJobObjectW(IntPtr.Zero,null); Check(job!=IntPtr.Zero);
            IntPtr attributes=IntPtr.Zero, jobValue=IntPtr.Zero, outHandle=IntPtr.Zero, errHandle=IntPtr.Zero, inHandle=IntPtr.Zero;
            bool initialized=false;
            PROCESS_INFO pi=new PROCESS_INFO();
            try {
                var limits=new EXTENDED_LIMIT(); limits.basic.flags=0x2000; // KILL_ON_JOB_CLOSE, no breakaway flags.
                Check(SetInformationJobObject(job,9,ref limits,(uint)Marshal.SizeOf<EXTENDED_LIMIT>()));
                IntPtr bytes=IntPtr.Zero;
                InitializeProcThreadAttributeList(IntPtr.Zero,1,0,ref bytes);
                if(bytes==IntPtr.Zero) throw new Win32Exception(Marshal.GetLastWin32Error());
                attributes=Marshal.AllocHGlobal(bytes);
                Check(InitializeProcThreadAttributeList(attributes,1,0,ref bytes)); initialized=true;
                jobValue=Marshal.AllocHGlobal(IntPtr.Size); Marshal.WriteIntPtr(jobValue,job);
                // Bind at CreateProcess time, before even a suspended child can be orphaned.
                Check(UpdateProcThreadAttribute(attributes,0,(IntPtr)0x2000d,jobValue,(IntPtr)IntPtr.Size,IntPtr.Zero,IntPtr.Zero));
                var sa=new SECURITY_ATTRIBUTES { nLength=Marshal.SizeOf<SECURITY_ATTRIBUTES>(), inherit=true };
                outHandle=CreateFileW(stdout,0x40000000,7,ref sa,2,0x80,IntPtr.Zero);
                errHandle=CreateFileW(stderr,0x40000000,7,ref sa,2,0x80,IntPtr.Zero);
                inHandle=CreateFileW("NUL",0x80000000,7,ref sa,3,0x80,IntPtr.Zero);
                Check(outHandle!=(IntPtr)(-1) && errHandle!=(IntPtr)(-1) && inHandle!=(IntPtr)(-1));
                var startup=new STARTUPINFOEX(); startup.info.cb=Marshal.SizeOf<STARTUPINFOEX>();
                startup.info.flags=0x100; startup.info.stdin=inHandle; startup.info.stdout=outHandle; startup.info.stderr=errHandle;
                startup.attributes=attributes;
                var command=new StringBuilder(Quote(exe)); foreach(string arg in args) command.Append(' ').Append(Quote(arg));
                Check(CreateProcessW(exe,command,IntPtr.Zero,IntPtr.Zero,true,0x08080004,IntPtr.Zero,cwd,ref startup,out pi));
                process=pi.process; Pid=pi.pid;
                Check(ResumeThread(pi.thread)!=uint.MaxValue);
            } catch { Dispose(); throw; }
            finally {
                if(pi.thread!=IntPtr.Zero) CloseHandle(pi.thread);
                foreach(var h in new[]{outHandle,errHandle,inHandle}) if(h!=IntPtr.Zero && h!=(IntPtr)(-1)) CloseHandle(h);
                if(initialized) DeleteProcThreadAttributeList(attributes);
                if(attributes!=IntPtr.Zero) Marshal.FreeHGlobal(attributes);
                if(jobValue!=IntPtr.Zero) Marshal.FreeHGlobal(jobValue);
            }
        }
        public bool HasExited { get { return process!=IntPtr.Zero && WaitForSingleObject(process,0)==0; } }
        public uint ExitCode { get { uint code; Check(GetExitCodeProcess(process,out code)); return code; } }
        public uint ActiveProcesses {
            get { ACCOUNTING a; Check(QueryInformationJobObject(job,1,out a,(uint)Marshal.SizeOf<ACCOUNTING>(),IntPtr.Zero)); return a.activeProcesses; }
        }
        public void Stop() {
            if(job==IntPtr.Zero) return;
            Check(TerminateJobObject(job,0));
            for(int n=0;n<100 && ActiveProcesses!=0;n++) Thread.Sleep(100);
            if(ActiveProcesses!=0) throw new Exception("The Job still contains active processes after termination.");
        }
        public void Dispose() {
            if(job!=IntPtr.Zero) { CloseHandle(job); job=IntPtr.Zero; }
            if(process!=IntPtr.Zero) { CloseHandle(process); process=IntPtr.Zero; }
        }
    }
}
'@
}
$scratch = Join-Path ([IO.Path]::GetTempPath()) ('orca-core-smoke-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $scratch -Force | Out-Null
$savedEnvironment = @{}
$coreJob = $null
function Invoke-OneShot([string]$Name, [string[]]$Arguments) {
    $job = [OrcaCoreSmoke.NativeJob]::new($nodeExe, $Arguments, $slot,
        (Join-Path $scratch "$Name.stdout"), (Join-Path $scratch "$Name.stderr"))
    try {
        $deadline = [DateTime]::UtcNow.AddSeconds(45)
        while (-not $job.HasExited -and [DateTime]::UtcNow -lt $deadline) { Start-Sleep -Milliseconds 100 }
        if (-not $job.HasExited -or $job.ExitCode -ne 0) { throw "$Name failed under the non-breakaway Job (output withheld: may contain secrets)." }
        $job.Stop()
    } finally { $job.Dispose() }
}
try {
    foreach ($name in @('ORCA_USER_DATA','ORCA_VERSION','ORCA_SERVE_RUNTIME','ELECTRON_RUN_AS_NODE','ORCA_APP_EXECUTABLE','NODE_OPTIONS')) {
        $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
        [Environment]::SetEnvironmentVariable($name, $null, 'Process')
    }
    $profile = Join-Path $scratch 'profile'
    [Environment]::SetEnvironmentVariable('ORCA_USER_DATA', $profile, 'Process')
    [Environment]::SetEnvironmentVariable('ORCA_VERSION', $manifest.slot_version, 'Process')
    [Environment]::SetEnvironmentVariable('ORCA_SERVE_RUNTIME', 'orcad', 'Process')
    Invoke-OneShot 'module-graph' @($entry, '--orcad-smoke-load-check')
    $ptyScript = @'
const pty=require(process.argv[1]);
const terminal=pty.spawn(process.env.ComSpec || 'cmd.exe',['/d','/s','/c','exit /b 0'],{cols:80,rows:24,cwd:process.cwd(),env:process.env});
const timer=setTimeout(()=>process.exit(2),20000);
terminal.onExit(({exitCode})=>{clearTimeout(timer);process.exit(exitCode===0?0:3)});
'@
    Invoke-OneShot 'conpty' @('-e', $ptyScript, (Join-Path $slot 'node_modules/node-pty'))
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = $listener.LocalEndpoint.Port
    $listener.Stop()
    $stdout = Join-Path $scratch 'serve.stdout'
    $coreJob = [OrcaCoreSmoke.NativeJob]::new($nodeExe,
        [string[]]@($entry, '--bind', '127.0.0.1', '--port', "$port", '--web-client-root', (Join-Path $CoreDirectory 'web'), '--json'),
        $slot, $stdout, (Join-Path $scratch 'serve.stderr'))
    $deadline = [DateTime]::UtcNow.AddSeconds($StartupTimeoutSeconds)
    $ready = $null
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($coreJob.HasExited) { throw "Core exited before readiness (exit $($coreJob.ExitCode))." }
        foreach ($line in @(Get-Content -LiteralPath $stdout -ErrorAction SilentlyContinue)) {
            if (-not $line.StartsWith('{')) { continue }
            try { $candidate = $line | ConvertFrom-Json -AsHashtable } catch { continue }
            if ($candidate.type -eq 'orca_server_ready') { $ready = $candidate; break }
        }
        if ($ready) { break }
        Start-Sleep -Milliseconds 150
    }
    if (-not $ready -or $ready.schemaVersion -ne 1) { throw 'No supported readiness event arrived.' }
    if ($ready.health.pid -ne $coreJob.Pid -or $ready.health.platform -ne 'win32' -or
        $ready.health.arch -ne 'x64' -or $ready.health.nodeVersion -ne $manifest.node_version -or
        $ready.health.buildVersion -ne $manifest.slot_version -or
        $ready.health.terminalDaemon.selfTest.ok -ne $true) {
        throw 'Core readiness failed PID, platform, pinned-runtime, version or terminal-health checks.'
    }
    $endpoint = [Uri]$ready.boundEndpoint
    if ($endpoint.Scheme -ne 'ws' -or $endpoint.Host -ne '127.0.0.1' -or $endpoint.Port -ne $port) {
        throw 'Core bound an unexpected endpoint.'
    }
    if ($ready.pairing.available -ne $true) { throw 'Core could not create a browser pairing offer.' }
    $webUrl = [Uri]$ready.pairing.webClientUrl
    if ($webUrl.Scheme -ne 'http' -or $webUrl.Host -ne '127.0.0.1' -or $webUrl.Port -ne $port -or
        $webUrl.AbsolutePath -ne '/web-index.html' -or -not $webUrl.Fragment.StartsWith('#pairing=')) {
        throw 'Core did not provide the expected local browser pairing URL.'
    }
    $response = Invoke-WebRequest -Uri "http://127.0.0.1:$port/web-index.html" -NoProxy -TimeoutSec 10
    if ($response.StatusCode -ne 200 -or $response.Headers['Content-Type'] -notlike '*text/html*' -or
        $response.Content -notmatch '(?i)<html|<!doctype html') { throw 'Browser client HTML is unavailable.' }
    $coreJob.Stop()
    $report = [ordered]@{
        schema_version = 1
        version = $manifest.version
        package_revision = $manifest['package_revision']
        target = $manifest.target
        checked_at = [DateTime]::UtcNow.ToString('o')
        runner = [Environment]::OSVersion.VersionString
        node_version = $manifest.node_version
        node_sha256 = $actualNodeHash
        plain_node_module_graph = 'passed'
        conpty_process_spawn = 'passed'
        readiness_pid_and_version = 'passed'
        terminal_self_test = 'passed'
        terminal_self_test_coverage = $ready.health.terminalDaemon.selfTest.coverage
        browser_html = 'passed'
        job_process_tree_cleanup = 'passed'
        webview2_interaction = 'not-tested-by-this-gate'
    }
    New-Item -ItemType Directory -Path (Split-Path -Parent ([IO.Path]::GetFullPath($ReportPath))) -Force | Out-Null
    $report | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $ReportPath -Encoding utf8NoBOM
    Write-Host 'Core qualification passed: plain Node, ConPTY, readiness, browser HTML, Job cleanup.'
} finally {
    if ($coreJob) { $coreJob.Dispose() }
    foreach ($entry in $savedEnvironment.GetEnumerator()) { [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process') }
    if (Test-Path -LiteralPath $scratch) { Remove-Item -LiteralPath $scratch -Recurse -Force }
}
