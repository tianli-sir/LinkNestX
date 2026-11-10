Unicode true
!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "WinVer.nsh"
!include "x64.nsh"
Name "LinkNestX Windows 7 (${APP_ARCH})"
OutFile "${APP_OUTPUT}"
InstallDir "$LOCALAPPDATA\Programs\LinkNestX-Legacy-${APP_ARCH}"
RequestExecutionLevel user
SetCompressor /SOLID lzma
ShowInstDetails show
ShowUninstDetails show
VIProductVersion "${APP_VERSION}.0"
VIAddVersionKey /LANG=1033 "ProductName" "LinkNestX Windows 7 Compatibility"
VIAddVersionKey /LANG=1033 "FileDescription" "LinkNestX Offline Installer (${APP_ARCH})"
VIAddVersionKey /LANG=1033 "FileVersion" "${APP_VERSION}"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\LinkNestX-Legacy.exe"
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "${APP_STAGE}\LICENSE-LinkNestX.txt"
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "SimpChinese"
!insertmacro MUI_LANGUAGE "English"

Function .onInit
  ${IfNot} ${AtLeastWin7}
    MessageBox MB_ICONSTOP "LinkNestX requires Windows 7 Service Pack 1 or later."
    Abort
  ${EndIf}
  ${If} ${IsWin7}
    ReadRegDWORD $0 HKLM "SYSTEM\CurrentControlSet\Control\Windows" "CSDVersion"
    ${If} $0 < 256
      MessageBox MB_ICONSTOP "Please install Windows 7 Service Pack 1 before installing LinkNestX."
      Abort
    ${EndIf}
  ${EndIf}
  !if "${APP_ARCH}" == "x64"
    ${IfNot} ${RunningX64}
      MessageBox MB_ICONSTOP "This package requires 64-bit Windows. Please use the x86 package."
      Abort
    ${EndIf}
  !endif
FunctionEnd

Section "LinkNestX" SEC_APP
  SetShellVarContext current
  System::Call 'kernel32::OpenMutexW(i 0x00100000, i 0, w "Local\LinkNestXLegacyRunning") p .r0 ?e'
  Pop $1
  ${If} $0 != 0
    System::Call 'kernel32::CloseHandle(p r0)'
    Goto running
  ${EndIf}
  ${If} $1 == 5
    Goto running
  ${EndIf}
  ; Do not kill a process with active transfers. Abort before replacing files.
  IfFileExists "$INSTDIR\LinkNestX-Legacy.exe" 0 install_files
  ClearErrors
  Rename "$INSTDIR\LinkNestX-Legacy.exe" "$INSTDIR\LinkNestX-Legacy.previous.exe"
  IfErrors running
  Rename "$INSTDIR\LinkNestX-Legacy.previous.exe" "$INSTDIR\LinkNestX-Legacy.exe"
  Goto install_files
  running:
    MessageBox MB_ICONSTOP "Please finish transfers and exit LinkNestX from the system tray before updating."
    Abort
  install_files:
    SetOutPath "$INSTDIR"
    File /r "${APP_STAGE}\*.*"
    WriteUninstaller "$INSTDIR\Uninstall.exe"
    CreateDirectory "$SMPROGRAMS\LinkNestX"
    CreateShortcut "$SMPROGRAMS\LinkNestX\LinkNestX (${APP_ARCH}).lnk" "$INSTDIR\LinkNestX-Legacy.exe"
    CreateShortcut "$DESKTOP\LinkNestX (${APP_ARCH}).lnk" "$INSTDIR\LinkNestX-Legacy.exe"
    WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\LinkNestX-Legacy-${APP_ARCH}" "DisplayName" "LinkNestX Windows 7 (${APP_ARCH})"
    WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\LinkNestX-Legacy-${APP_ARCH}" "DisplayVersion" "${APP_VERSION}"
    WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\LinkNestX-Legacy-${APP_ARCH}" "Publisher" "LinkNestX contributors"
    WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\LinkNestX-Legacy-${APP_ARCH}" "UninstallString" '$"$INSTDIR\Uninstall.exe$"'
    WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\LinkNestX-Legacy-${APP_ARCH}" "InstallLocation" "$INSTDIR"
    WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\LinkNestX-Legacy-${APP_ARCH}" "NoModify" 1
    WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\LinkNestX-Legacy-${APP_ARCH}" "NoRepair" 1
SectionEnd

Section "Uninstall"
  SetShellVarContext current
  System::Call 'kernel32::OpenMutexW(i 0x00100000, i 0, w "Local\LinkNestXLegacyRunning") p .r0 ?e'
  Pop $1
  ${If} $0 != 0
    System::Call 'kernel32::CloseHandle(p r0)'
    MessageBox MB_ICONSTOP "Please finish transfers and exit LinkNestX from the system tray before uninstalling."
    Abort
  ${EndIf}
  ${If} $1 == 5
    MessageBox MB_ICONSTOP "LinkNestX may be running in another session. Please close it first."
    Abort
  ${EndIf}
  ClearErrors
  Delete "$INSTDIR\LinkNestX-Legacy.exe"
  IfErrors 0 remove_files
    MessageBox MB_ICONSTOP "Please exit LinkNestX from the system tray before uninstalling."
    Abort
  remove_files:
    ; Match the exact commands emitted by Electron. Never remove another
    ; installation's startup entry or an entry that somebody has repurposed.
    !if "${APP_ARCH}" == "x64"
      SetRegView 64
    !endif
    ReadRegStr $0 HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "LinkNestX-Legacy-${APP_ARCH}"
    StrCmp $0 '$"$INSTDIR\LinkNestX-Legacy.exe$" --hidden' remove_startup
    StrCmp $0 '$INSTDIR\LinkNestX-Legacy.exe --hidden' remove_startup keep_startup
    remove_startup:
      DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "LinkNestX-Legacy-${APP_ARCH}"
    keep_startup:
    SetRegView 32
    Delete "$DESKTOP\LinkNestX (${APP_ARCH}).lnk"
    Delete "$SMPROGRAMS\LinkNestX\LinkNestX (${APP_ARCH}).lnk"
    RMDir "$SMPROGRAMS\LinkNestX"
    DeleteRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\LinkNestX-Legacy-${APP_ARCH}"
    ; Only the files listed by the packager are removed. User data is retained.
    !include "${APP_UNINSTALL_LIST}"
    Delete "$INSTDIR\Uninstall.exe"
    RMDir "$INSTDIR"
SectionEnd
