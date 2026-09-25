; GameNoLag Windows installer.
;
; A wizard around install.ps1 rather than a second implementation of it. The
; script is where the decisions about who may read and write what are made and
; explained; duplicating them here would give two places to get them wrong.
;
; Build (Inno Setup 6), from the directory holding the five-file bundle:
;   iscc /DAppVersion=1.2.3 /DPayloadDir=path\to\bundle /DDefaultControlUrl=https://cp.example.com gamenolag.iss
;
; Silent install, for somebody deploying to several machines:
;   GameNoLag-Setup.exe /VERYSILENT /KEY=GNL-XXXX-XXXX-XXXX /URL=https://cp.example.com

#ifndef AppVersion
  #define AppVersion "0.0.0-dev"
#endif
#ifndef PayloadDir
  #define PayloadDir "."
#endif
#ifndef DefaultControlUrl
  #define DefaultControlUrl ""
#endif

[Setup]
; Fixed forever: it is how Windows recognises an upgrade of the same product.
AppId={{6F1B2C4E-9D7A-4E3B-8C51-2A9E7D0F3B61}
AppName=GameNoLag
AppVersion={#AppVersion}
AppPublisher=GameNoLag contributors
AppPublisherURL=https://github.com/hashcott/NoLag
AppSupportURL=https://github.com/hashcott/NoLag/issues
; install.ps1 always installs here, so the directory is not the user's to pick.
DefaultDirName={autopf}\GameNoLag
DisableDirPage=yes
DisableProgramGroupPage=yes
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
OutputBaseFilename=GameNoLag-Setup-{#AppVersion}
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
UninstallDisplayName=GameNoLag
UninstallDisplayIcon={app}\gnl-ui.exe
LicenseFile={#PayloadDir}\LICENSE

[Languages]
Name: "en"; MessagesFile: "compiler:Default.isl"

[Files]
; The payload goes to a temporary directory and install.ps1 copies it into place.
; It stops a running service and tray first, which Inno writing straight into
; Program Files would not do, and a running binary cannot be overwritten.
Source: "{#PayloadDir}\gnl-service.exe";     DestDir: "{tmp}\payload"; Flags: deleteafterinstall
Source: "{#PayloadDir}\gnl-ui.exe";          DestDir: "{tmp}\payload"; Flags: deleteafterinstall
Source: "{#PayloadDir}\gnl-ui.exe.manifest"; DestDir: "{tmp}\payload"; Flags: deleteafterinstall
Source: "{#PayloadDir}\install.ps1";         DestDir: "{tmp}\payload"; Flags: deleteafterinstall
; Kept, so that Settings → Apps → Uninstall has something to run.
Source: "{#PayloadDir}\uninstall.ps1";       DestDir: "{app}"

[Run]
; runasoriginaluser: the tray holds no privilege and belongs in the player's
; session, not the elevated one this installer runs in.
Filename: "{app}\gnl-ui.exe"; Description: "Start GameNoLag"; \
  Flags: postinstall nowait skipifsilent runasoriginaluser

[UninstallRun]
Filename: "{sys}\WindowsPowerShell\v1.0\powershell.exe"; \
  Parameters: "-NoProfile -ExecutionPolicy Bypass -File ""{app}\uninstall.ps1"""; \
  Flags: runhidden waituntilterminated; RunOnceId: "GameNoLagUninstall"

[Code]
var
  AccessPage: TInputQueryWizardPage;
  InstallFailed: Boolean;

// Both values end up on a PowerShell command line. Refusing anything outside a
// narrow alphabet is what keeps a pasted value from becoming a second command.
function IsKey(const S: String): Boolean;
var
  I: Integer;
  C: Char;
begin
  Result := (Length(S) >= 8) and (Copy(S, 1, 4) = 'GNL-');
  for I := 1 to Length(S) do
  begin
    C := S[I];
    if not (((C >= 'A') and (C <= 'Z')) or ((C >= '0') and (C <= '9')) or (C = '-')) then
      Result := False;
  end;
end;

function IsControlUrl(const S: String): Boolean;
var
  I: Integer;
  C: Char;
begin
  Result := (Length(S) > 8) and (Copy(S, 1, 8) = 'https://');
  for I := 1 to Length(S) do
  begin
    C := S[I];
    if not (((C >= 'a') and (C <= 'z')) or ((C >= 'A') and (C <= 'Z')) or
            ((C >= '0') and (C <= '9')) or (Pos(C, '.-:/_') > 0)) then
      Result := False;
  end;
end;

// A path inside a PowerShell single-quoted string. The temporary directory sits
// under the user's profile, and a name like O'Brien would otherwise end it.
function PSQuote(const S: String): String;
begin
  Result := S;
  StringChangeEx(Result, '''', '''''', True);
  Result := '''' + Result + '''';
end;

function Key: String;
begin
  Result := Uppercase(Trim(AccessPage.Values[0]));
end;

function ControlUrl: String;
begin
  Result := Trim(AccessPage.Values[1]);
end;

procedure InitializeWizard;
var
  url: String;
begin
  AccessPage := CreateInputQueryPage(wpLicense,
    'Your GameNoLag access',
    'Enter the details you were given when you contributed a relay.',
    'The key activates this PC as one of your devices. The address is the ' +
    'GameNoLag control plane, and must start with https://.');
  AccessPage.Add('Contributor key (GNL-XXXX-XXXX-XXXX-XXXX):', False);
  AccessPage.Add('Control-plane address:', False);
  AccessPage.Values[0] := ExpandConstant('{param:KEY|}');
  url := ExpandConstant('{param:URL|}');
  if url = '' then
    url := '{#DefaultControlUrl}';
  AccessPage.Values[1] := url;
end;

function CheckAccess: String;
begin
  Result := '';
  if not IsKey(Key) then
    Result := 'That does not look like a contributor key. It starts with GNL- and ' +
              'contains only letters, digits and dashes.'
  else if not IsControlUrl(ControlUrl) then
    Result := 'The control-plane address must start with https://. Over plain ' +
              'http your key would travel in clear.';
end;

function NextButtonClick(CurPageID: Integer): Boolean;
var
  problem: String;
begin
  Result := True;
  if CurPageID = AccessPage.ID then
  begin
    problem := CheckAccess;
    if problem <> '' then
    begin
      MsgBox(problem, mbError, MB_OK);
      Result := False;
    end;
  end;
end;

// Runs after the wizard and before anything is written, silent or not. A
// silent install never shows the page, so this is where its /KEY= and /URL=
// are checked.
function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  Result := CheckAccess;
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  ps, log, args: String;
  code: Integer;
  output: AnsiString;
begin
  if CurStep <> ssPostInstall then
    exit;
  ps := ExpandConstant('{sys}\WindowsPowerShell\v1.0\powershell.exe');
  log := ExpandConstant('{tmp}\install.log');
  // A throw inside install.ps1 ends the & call; the catch records why, so the
  // message below can say more than an exit code.
  args := '-NoProfile -ExecutionPolicy Bypass -Command "' +
    'try { & ' + PSQuote(ExpandConstant('{tmp}\payload\install.ps1')) +
    ' -ContributorKey ''' + Key + ''' -ControlUrl ''' + ControlUrl + '''' +
    ' *>&1 | Out-File -Encoding ascii -FilePath ' + PSQuote(log) + '; exit 0 }' +
    ' catch { $_ | Out-File -Encoding ascii -Append -FilePath ' + PSQuote(log) + '; exit 1 }"';
  if not Exec(ps, args, '', SW_HIDE, ewWaitUntilTerminated, code) or (code <> 0) then
  begin
    InstallFailed := True;
    if not LoadStringFromFile(log, output) then
      output := '';
    // Kept on one line: a line starting with # is a preprocessor directive.
    SuppressibleMsgBox('GameNoLag could not be set up (exit code ' + IntToStr(code) + ').' + #13#10#13#10 + String(output),
      mbCriticalError, MB_OK, IDOK);
  end;
end;

// So that a silent deployment script can tell a failed install from a good one.
function GetCustomSetupExitCode: Integer;
begin
  if InstallFailed then
    Result := 1
  else
    Result := 0;
end;
