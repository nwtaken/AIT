Unicode true

####
## AIT installer: per-user install (no admin prompt).
## Pages: Welcome, Folder, Components, Install, Finish.
## Built by `wails build -nsis`; the ${INFO_*} values come from wails.json.
####

!define WAILS_INSTALL_SCOPE "user"
!define REQUEST_EXECUTION_LEVEL "user"
!include "wails_tools.nsh"

VIProductVersion "${INFO_PRODUCTVERSION}.0"
VIFileVersion    "${INFO_PRODUCTVERSION}.0"
VIAddVersionKey "CompanyName"     "${INFO_COMPANYNAME}"
VIAddVersionKey "FileDescription" "${INFO_PRODUCTNAME} Setup"
VIAddVersionKey "ProductVersion"  "${INFO_PRODUCTVERSION}"
VIAddVersionKey "FileVersion"     "${INFO_PRODUCTVERSION}"
VIAddVersionKey "LegalCopyright"  "${INFO_COPYRIGHT}"
VIAddVersionKey "ProductName"     "${INFO_PRODUCTNAME}"

ManifestDPIAware true

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "FileFunc.nsh"

!define MUI_ICON "..\icon.ico"
!define MUI_UNICON "..\icon.ico"
!define MUI_WELCOMEFINISHPAGE_BITMAP "side.bmp"
!define MUI_UNWELCOMEFINISHPAGE_BITMAP "side.bmp"
!define MUI_HEADERIMAGE
!define MUI_HEADERIMAGE_RIGHT
!define MUI_HEADERIMAGE_BITMAP "header.bmp"
!define MUI_ABORTWARNING

!define MUI_WELCOMEPAGE_TITLE "AIT Setup"
!define MUI_WELCOMEPAGE_TEXT "This will install AIT ${INFO_PRODUCTVERSION} on your computer.$\r$\n$\r$\nIf AIT is running, it will be closed during setup.$\r$\n$\r$\nClick Next to continue."
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!define MUI_COMPONENTSPAGE_SMALLDESC
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "$INSTDIR\${PRODUCT_EXECUTABLE}"
!define MUI_FINISHPAGE_RUN_TEXT "Run AIT"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_COMPONENTS
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

Name "${INFO_PRODUCTNAME}"
Caption "AIT Setup"
BrandingText "AIT ${INFO_PRODUCTVERSION}"
OutFile "..\..\bin\${INFO_PROJECTNAME}-${ARCH}-installer.exe"
InstallDir "$LOCALAPPDATA\Programs\${INFO_PRODUCTNAME}"
ShowInstDetails nevershow
ShowUninstDetails nevershow

Function .onInit
    !insertmacro wails.checkArchitecture
FunctionEnd

# AIT's updater runs this installer with /S /RELAUNCH: start AIT again after.
Function .onInstSuccess
    ${GetParameters} $R0
    ClearErrors
    ${GetOptions} $R0 "/RELAUNCH" $R1
    ${IfNot} ${Errors}
        Exec '"$INSTDIR\${PRODUCT_EXECUTABLE}"'
    ${EndIf}
FunctionEnd

# A running copy would lock the files being replaced.
!macro closeAIT
    nsExec::Exec 'taskkill /IM "${PRODUCT_EXECUTABLE}" /F'
    Pop $0
    Sleep 300
!macroend

Section "AIT (required)" SecApp
    SectionIn RO
    !insertmacro wails.setShellContext
    !insertmacro closeAIT
    !insertmacro wails.webview2runtime
    SetOutPath $INSTDIR
    !insertmacro wails.files
    !insertmacro wails.associateFiles
    !insertmacro wails.associateCustomProtocols
    !insertmacro wails.writeUninstaller
SectionEnd

Section "Start Menu shortcut" SecStart
    CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
SectionEnd

Section "Desktop shortcut" SecDesktop
    CreateShortcut "$DESKTOP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
SectionEnd

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
    !insertmacro MUI_DESCRIPTION_TEXT ${SecApp} "Program files."
    !insertmacro MUI_DESCRIPTION_TEXT ${SecStart} "Add AIT to the Start Menu."
    !insertmacro MUI_DESCRIPTION_TEXT ${SecDesktop} "Add an AIT shortcut to the desktop."
!insertmacro MUI_FUNCTION_DESCRIPTION_END

Section "un.AIT" UnApp
    SectionIn RO
    !insertmacro wails.setShellContext
    !insertmacro closeAIT
    RMDir /r "$AppData\${PRODUCT_EXECUTABLE}" # the WebView2 cache
    RMDir /r $INSTDIR
    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"
    !insertmacro wails.unassociateFiles
    !insertmacro wails.unassociateCustomProtocols
    !insertmacro wails.deleteUninstaller
SectionEnd

Section /o "un.Settings and chat history" UnData
    RMDir /r "$APPDATA\AIT"
SectionEnd

!insertmacro MUI_UNFUNCTION_DESCRIPTION_BEGIN
    !insertmacro MUI_DESCRIPTION_TEXT ${UnApp} "Program files and shortcuts."
    !insertmacro MUI_DESCRIPTION_TEXT ${UnData} "Also delete AIT settings, added accounts and chat history. Claude and Codex logins are not affected."
!insertmacro MUI_UNFUNCTION_DESCRIPTION_END
