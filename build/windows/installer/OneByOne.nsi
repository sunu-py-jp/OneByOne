; Generated values and explicit payload file lists are supplied by
; scripts/windows-installer.mjs. No downloads happen on the user's machine.
Unicode true
RequestExecutionLevel user
ManifestDPIAware true
SetCompressor /FINAL lzma
SetCompressorDictSize 16
AllowSkipFiles off
Name "OneByOne"
OutFile "${OBO_OUTPUT}"
InstallDir "$LOCALAPPDATA\Programs\OneByOne"
InstallDirRegKey HKCU "Software\OneByOne\Installer" "InstallDir"
Icon "${OBO_ICON}"
UninstallIcon "${OBO_ICON}"
VIProductVersion "${OBO_FILE_VERSION}"
VIAddVersionKey "ProductName" "OneByOne"
VIAddVersionKey "FileDescription" "OneByOne Setup"
VIAddVersionKey "FileVersion" "${OBO_VERSION}"
VIAddVersionKey "LegalCopyright" "OneByOne"

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "x64.nsh"
!include "WordFunc.nsh"
!include "FileFunc.nsh"

!define OBO_UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\OneByOne"
!define OBO_WEBVIEW2_KEY "Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"
; Keep aligned with Wails v2.15.0 internal/wv2installer.MinimumRuntimeVersion.
!define OBO_MIN_WEBVIEW2 "94.0.992.31"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\OneByOne.exe"
!define MUI_FINISHPAGE_RUN_NOTCHECKED
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_UNPAGE_FINISH
!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "Japanese"

Var WebView2Version
Var WebView2ExitCode

!macro OBO_REQUIRE_REAL_PATH PATH
  System::Call 'kernel32::GetFileAttributesW(w "${PATH}") i.r8'
  ${If} $8 != -1
    IntOp $8 $8 & 0x400 ; FILE_ATTRIBUTE_REPARSE_POINT
    ${If} $8 != 0
      MessageBox MB_ICONSTOP "Setup cannot modify linked application paths: ${PATH}" /SD IDOK
      SetErrorLevel 1
      Abort
    ${EndIf}
  ${EndIf}
!macroend

Function CheckPayloadPaths
  !include "${OBO_CHECK_FILES}"
  !insertmacro OBO_REQUIRE_REAL_PATH "$INSTDIR\Uninstall.exe"
FunctionEnd

Function un.CheckPayloadPaths
  !include "${OBO_CHECK_FILES}"
FunctionEnd

!macro OBO_CHECK_EXECUTABLE FILE LABEL
  ${LABEL}_retry:
  IfFileExists "$INSTDIR\${FILE}" 0 ${LABEL}_done
  ; Request write access without changing the file. A running executable or
  ; a read-only install must be handled before changing any shipped files.
  System::Call 'kernel32::CreateFileW(w "$INSTDIR\${FILE}", i 0x40000000, i 0, p 0, i 3, i 0x80, p 0) p.r0'
  ${If} $0 == -1
    MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "Close OneByOne and its command-line processes, and check that this folder is writable: $INSTDIR" /SD IDCANCEL IDRETRY ${LABEL}_retry
    SetErrorLevel 1
    Abort
  ${EndIf}
  System::Call 'kernel32::CloseHandle(p r0)'
  ${LABEL}_done:
!macroend

Function CheckApplicationClosed
  !insertmacro OBO_CHECK_EXECUTABLE "OneByOne.exe" gui
  !insertmacro OBO_CHECK_EXECUTABLE "onebyone-cli.exe" cli
FunctionEnd

Function un.CheckApplicationClosed
  !insertmacro OBO_CHECK_EXECUTABLE "OneByOne.exe" gui
  !insertmacro OBO_CHECK_EXECUTABLE "onebyone-cli.exe" cli
FunctionEnd

Function .onInit
  SetShellVarContext current
  ${IfNot} ${RunningX64}
    MessageBox MB_ICONSTOP "This installer requires 64-bit Windows."
    Abort
  ${EndIf}
  !if "${OBO_ARCH}" == "arm64"
    ${IfNot} ${IsNativeARM64}
      MessageBox MB_ICONSTOP "This installer requires Windows on ARM64."
      Abort
    ${EndIf}
  !else
    ${If} ${IsNativeARM64}
      MessageBox MB_ICONSTOP "Please use the ARM64 OneByOne installer on this PC."
      Abort
    ${EndIf}
  !endif
FunctionEnd

; Wails must be able to start with the installed runtime. A nonempty but old
; pv is insufficient; newer runtimes are left unchanged (never downgraded).
!macro OBO_READ_WEBVIEW2 ROOT
  ReadRegStr $0 ${ROOT} "${OBO_WEBVIEW2_KEY}" "pv"
  ${If} $0 != ""
  ${AndIf} $0 != "null"
    ${VersionCompare} "$0" "${OBO_MIN_WEBVIEW2}" $1
    ${If} $1 != 2
      StrCpy $WebView2Version $0
    ${EndIf}
  ${EndIf}
!macroend

Function DetectWebView2
  StrCpy $WebView2Version ""
  SetRegView 32
  !insertmacro OBO_READ_WEBVIEW2 HKCU
  !insertmacro OBO_READ_WEBVIEW2 HKLM
  SetRegView 64
  !insertmacro OBO_READ_WEBVIEW2 HKCU
  !insertmacro OBO_READ_WEBVIEW2 HKLM
  SetRegView 32
FunctionEnd

Function RemovePreviousVersion
  IfFileExists "$INSTDIR\Uninstall.exe" previous_installer no_previous_installer
  previous_installer:
  ReadRegStr $0 HKCU "Software\OneByOne\Installer" "InstallDir"
  ${If} $0 != $INSTDIR
    MessageBox MB_ICONSTOP "This folder contains an unmanaged installation. Move it before installing OneByOne: $INSTDIR" /SD IDOK
    SetErrorLevel 1
    Abort
  ${EndIf}
  DetailPrint "Removing the previous OneByOne application files..."
  ; _?= must be the final, unquoted argument, including paths with spaces.
  ; It makes NSIS wait for the real uninstaller, instead of a spawned copy.
  ClearErrors
  ExecWait '"$INSTDIR\Uninstall.exe" /S _?=$INSTDIR' $0
  ${If} ${Errors}
  ${OrIf} $0 != 0
    MessageBox MB_ICONSTOP "The previous version could not be removed. Close OneByOne and retry this setup." /SD IDOK
    SetErrorLevel 1
    Abort
  ${EndIf}
  Return
  no_previous_installer:
  IfFileExists "$INSTDIR\tools\git\*.*" unmanaged_git no_unmanaged_git
  unmanaged_git:
    MessageBox MB_ICONSTOP "This folder contains unmanaged Git files. Move them before installing OneByOne: $INSTDIR\tools\git" /SD IDOK
    SetErrorLevel 1
    Abort
  no_unmanaged_git:
FunctionEnd

Function EnsureWebView2
  Call DetectWebView2
  ${If} $WebView2Version != ""
    DetailPrint "Using installed WebView2 Runtime ($WebView2Version)."
    Return
  ${EndIf}
  InitPluginsDir
  SetOutPath "$PLUGINSDIR"
  ; The Microsoft payload is already compressed. Avoid recompressing hundreds
  ; of MB on every build, or decompressing it when the runtime already exists.
  SetCompress off
  File /oname=WebView2Standalone.exe "${OBO_WEBVIEW2_INSTALLER}"
  SetCompress auto
  DetailPrint "Installing bundled Microsoft WebView2 Runtime..."
  ClearErrors
  ExecWait '"$PLUGINSDIR\WebView2Standalone.exe" /silent /install' $WebView2ExitCode
  ${If} ${Errors}
    MessageBox MB_ICONSTOP "WebView2 could not be started. OneByOne has not been installed." /SD IDOK
    SetErrorLevel 1
    Abort
  ${EndIf}
  ${If} $WebView2ExitCode == 3010
  ${OrIf} $WebView2ExitCode == 1641
    SetRebootFlag true
  ${ElseIf} $WebView2ExitCode != 0
    MessageBox MB_ICONSTOP "WebView2 installation failed (exit $WebView2ExitCode). Check your device's installation policy, then run this setup again." /SD IDOK
    SetErrorLevel $WebView2ExitCode
    Abort
  ${EndIf}
  Call DetectWebView2
  ${If} $WebView2Version == ""
    ${If} ${RebootFlag}
      MessageBox MB_ICONINFORMATION "Restart Windows, then run this setup again to finish installing OneByOne." /SD IDOK
      SetErrorLevel 3010
    ${Else}
      MessageBox MB_ICONSTOP "WebView2 installation could not be verified. OneByOne has not been installed. Check your device's installation policy." /SD IDOK
      SetErrorLevel 1
    ${EndIf}
    Abort
  ${EndIf}
FunctionEnd

Section "OneByOne" MainSection
  Call CheckPayloadPaths
  Call CheckApplicationClosed
  Call EnsureWebView2
  Call RemovePreviousVersion
  ; The previous uninstaller removes only its shipped files, including helpers
  ; no longer present in this release. User-created files remain untouched.
  SetOverwrite on
  !include "${OBO_INSTALL_FILES}"
  SetOutPath "$INSTDIR"
  WriteUninstaller "$INSTDIR\Uninstall.exe"
  CreateDirectory "$SMPROGRAMS\OneByOne"
  CreateShortcut "$SMPROGRAMS\OneByOne\OneByOne.lnk" "$INSTDIR\OneByOne.exe"
  CreateShortcut "$SMPROGRAMS\OneByOne\Uninstall OneByOne.lnk" "$INSTDIR\Uninstall.exe"
  WriteRegStr HKCU "Software\OneByOne\Installer" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${OBO_UNINSTALL_KEY}" "DisplayName" "OneByOne"
  WriteRegStr HKCU "${OBO_UNINSTALL_KEY}" "DisplayVersion" "${OBO_VERSION}"
  WriteRegStr HKCU "${OBO_UNINSTALL_KEY}" "Publisher" "OneByOne"
  WriteRegStr HKCU "${OBO_UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${OBO_UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\OneByOne.exe"
  WriteRegStr HKCU "${OBO_UNINSTALL_KEY}" "UninstallString" '$\"$INSTDIR\Uninstall.exe$\"'
  WriteRegStr HKCU "${OBO_UNINSTALL_KEY}" "QuietUninstallString" '$\"$INSTDIR\Uninstall.exe$\" /S'
  WriteRegDWORD HKCU "${OBO_UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${OBO_UNINSTALL_KEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  WriteRegDWORD HKCU "${OBO_UNINSTALL_KEY}" "EstimatedSize" $0
  ${If} ${RebootFlag}
    SetErrorLevel 3010
  ${EndIf}
SectionEnd

Function un.onInit
  SetShellVarContext current
FunctionEnd

Section "Uninstall"
  Call un.CheckPayloadPaths
  Call un.CheckApplicationClosed
  !include "${OBO_UNINSTALL_FILES}"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\OneByOne\OneByOne.lnk"
  Delete "$SMPROGRAMS\OneByOne\Uninstall OneByOne.lnk"
  RMDir "$SMPROGRAMS\OneByOne"
  DeleteRegKey HKCU "${OBO_UNINSTALL_KEY}"
  DeleteRegKey HKCU "Software\OneByOne\Installer"
  ; Keep shared WebView2 and %LOCALAPPDATA%\OneByOne (workspaces/credentials).
SectionEnd
