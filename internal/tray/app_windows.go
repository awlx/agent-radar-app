//go:build windows

package tray

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"

	"fyne.io/systray"
	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const Supported = true

const appName = "Copilot Radar"

var (
	user32                         = windows.NewLazySystemDLL("user32.dll")
	pSetWindowPos                  = user32.NewProc("SetWindowPos")
	pShowWindow                    = user32.NewProc("ShowWindow")
	pSetForegroundWindow           = user32.NewProc("SetForegroundWindow")
	pIsIconic                      = user32.NewProc("IsIconic")
	pIsWindowVisible               = user32.NewProc("IsWindowVisible")
	pEnumWindows                   = user32.NewProc("EnumWindows")
	pGetWindowThreadProcessID      = user32.NewProc("GetWindowThreadProcessId")
	pGetWindow                     = user32.NewProc("GetWindow")
	pGetWindowTextLength           = user32.NewProc("GetWindowTextLengthW")
	pPostMessage                   = user32.NewProc("PostMessageW")
	pSendMessage                   = user32.NewProc("SendMessageW")
	pCreateIconFromResourceEx      = user32.NewProc("CreateIconFromResourceEx")
	pSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	pGetDpiForSystem               = user32.NewProc("GetDpiForSystem")
	pGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	pFindWindow                    = user32.NewProc("FindWindowW")
	pDestroyWindow                 = user32.NewProc("DestroyWindow")
	pMessageBox                    = user32.NewProc("MessageBoxW")
	pDwmSetWindowAttribute         = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
	pCoInitializeEx                = windows.NewLazySystemDLL("ole32.dll").NewProc("CoInitializeEx")
)

const (
	wmClose     = 0x0010
	wmSetIcon   = 0x0080
	swShow      = 5
	swRestore   = 9
	gwOwner     = 4
	smCxScreen  = 0
	smCyScreen  = 1
	smCxIcon    = 11
	smCxSmIcon  = 49
	swpNoSize   = 0x0001
	swpNoMove   = 0x0002
	swpNoActive = 0x0010
	mbIconInfo  = 0x40

	settingsKey = `Software\Copilot Radar`
	runKey      = `Software\Microsoft\Windows\CurrentVersion\Run`
	themeKey    = `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`
	stayValue   = "StayInForeground"
	maxRows     = 8 // the maximum number of sessions and of models in the menu
)

// call calls a Windows function if it exists. Old Windows versions do not have all the functions.
func call(p *windows.LazyProc, args ...uintptr) uintptr {
	if p.Find() != nil {
		return 0
	}
	r, _, _ := p.Call(args...)
	return r
}

func utf16(s string) uintptr {
	p, _ := windows.UTF16PtrFromString(s)
	return uintptr(unsafe.Pointer(p))
}

// dataDir is the folder for the log file and the WebView2 data: %LOCALAPPDATA%\Copilot Radar.
func dataDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, appName)
}

// A Windows GUI program has no standard error. The log then goes to a file in dataDir.
func init() {
	if h, err := windows.GetStdHandle(windows.STD_ERROR_HANDLE); err == nil && h != 0 && h != windows.InvalidHandle {
		return
	}
	dir := dataDir()
	_ = os.MkdirAll(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, "copilot-radar.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	if fi, err := f.Stat(); err == nil && fi.Size() > 1<<20 {
		_ = f.Truncate(0)
	}
	log.SetOutput(f)
}

func regBool(name string) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, settingsKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(name)
	return err == nil && v != 0
}

func setRegBool(name string, on bool) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, settingsKey, registry.SET_VALUE)
	if err != nil {
		log.Printf("settings: %v", err)
		return
	}
	defer k.Close()
	v := uint32(0)
	if on {
		v = 1
	}
	_ = k.SetDWordValue(name, v)
}

func startsAtLogin() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(appName)
	return err == nil && v != ""
}

// setStartAtLogin adds the app to the programs that Windows starts at login.
// The app then starts with the tray icon only.
func setStartAtLogin(on bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		log.Printf("start at login: %v", err)
		return
	}
	defer k.Close()
	if !on {
		_ = k.DeleteValue(appName)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		log.Printf("start at login: %v", err)
		return
	}
	_ = k.SetStringValue(appName, `"`+exe+`" -background`)
}

// inkOf gives the color of the radar for the taskbar: dark on a light taskbar, and white on a dark taskbar.
func inkOf() rgba {
	k, err := registry.OpenKey(registry.CURRENT_USER, themeKey, registry.QUERY_VALUE)
	if err == nil {
		defer k.Close()
		if v, _, err := k.GetIntegerValue("SystemUsesLightTheme"); err == nil && v == 1 {
			return rgba{0.1, 0.1, 0.1, 1}
		}
	}
	return rgba{1, 1, 1, 1}
}

func activate(h uintptr) {
	if call(pIsIconic, h) != 0 {
		call(pShowWindow, h, swRestore)
	} else {
		call(pShowWindow, h, swShow)
	}
	call(pSetForegroundWindow, h)
}

func shellOpen(target string) error {
	return windows.ShellExecute(0, windows.StringToUTF16Ptr("open"), windows.StringToUTF16Ptr(target), nil, nil, swShow)
}

// editorPaths gives the program files of the VS Code editors, by the name of their data folder.
var editorPaths = map[string][]string{
	"Code":            {`Microsoft VS Code\Code.exe`},
	"Code - Insiders": {`Microsoft VS Code Insiders\Code - Insiders.exe`},
	"Cursor":          {`cursor\Cursor.exe`},
	"VSCodium":        {`VSCodium\VSCodium.exe`},
}

func editorExe(editor string) string {
	for _, rel := range editorPaths[editor] {
		for _, base := range []string{filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs"), os.Getenv("ProgramFiles")} {
			if p := filepath.Join(base, rel); base != "" && fileExists(p) {
				return p
			}
		}
	}
	return ""
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

var (
	enumMu  sync.Mutex
	enumPID uint32
	enumHit uintptr
	enumCB  = windows.NewCallback(func(h, _ uintptr) uintptr {
		var pid uint32
		call(pGetWindowThreadProcessID, h, uintptr(unsafe.Pointer(&pid)))
		if pid == enumPID && call(pIsWindowVisible, h) != 0 && call(pGetWindow, h, gwOwner) == 0 && call(pGetWindowTextLength, h) > 0 {
			enumHit = h
			return 0
		}
		return 1
	})
)

// windowOf finds the main window that shows a process. It walks up the parent processes,
// for example from the Copilot CLI to the shell and then to Windows Terminal.
func windowOf(pid int) uintptr {
	if pid <= 0 {
		return 0
	}
	parents := map[uint32]uint32{}
	if snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0); err == nil {
		var e windows.ProcessEntry32
		e.Size = uint32(unsafe.Sizeof(e))
		for err := windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
			parents[e.ProcessID] = e.ParentProcessID
		}
		windows.CloseHandle(snap)
	}
	enumMu.Lock()
	defer enumMu.Unlock()
	p := uint32(pid)
	for i := 0; p > 4 && i < 32; i++ {
		enumPID, enumHit = p, 0
		call(pEnumWindows, enumCB, 0)
		if enumHit != 0 {
			return enumHit
		}
		p = parents[p]
	}
	return 0
}

// menuRow is a menu item that shows a line of text or is hidden. It changes the item only when the text changes.
type menuRow struct {
	item    *systray.MenuItem
	title   string
	visible bool
}

func (r *menuRow) set(title string, visible bool) {
	if visible && title != r.title {
		r.item.SetTitle(title)
		r.title = title
	}
	if visible != r.visible {
		if visible {
			r.item.Show()
		} else {
			r.item.Hide()
		}
		r.visible = visible
	}
}

// setRows shows the lines below a header. If there are more lines than rows, the last row tells how many lines it does not show.
func setRows(head *menuRow, rows []*menuRow, title string, lines []string) {
	if len(lines) > len(rows) {
		more := len(lines) - len(rows) + 1
		lines = append(append([]string{}, lines[:len(rows)-1]...), fmt.Sprintf("… and %d more", more))
	}
	head.set(title, len(lines) > 0)
	for i, r := range rows {
		if i < len(lines) {
			r.set("      "+lines[i], true)
		} else {
			r.set("", false)
		}
	}
}

type winApp struct {
	url, version string
	showAtStart  bool

	mu      sync.Mutex
	hwnd    uintptr // the radar window, or 0
	opening bool
	waits   []jumpTarget
	icon    iconKey
	icons   map[iconSize]uintptr // window icons

	status, waitHead, modelHead *menuRow
	waitRows, modelRows         []*menuRow
	stay, login                 *systray.MenuItem
}

func onClick(item *systray.MenuItem, f func()) {
	go func() {
		for range item.ClickedCh {
			f()
		}
	}()
}

func newRow(title, tooltip string, enabled bool) *menuRow {
	it := systray.AddMenuItem(title, tooltip)
	if !enabled {
		it.Disable()
	}
	r := &menuRow{item: it, title: title, visible: true}
	return r
}

func (a *winApp) buildMenu() {
	systray.SetTooltip(appName)
	a.status = newRow("No active agents", "", false)
	a.waitHead = newRow("Waiting for you", "", false)
	a.waitHead.set("", false)
	for i := 0; i < maxRows; i++ {
		r := newRow("", "Show the app of this session", true)
		r.set("", false)
		onClick(r.item, func() { a.jump(i) })
		a.waitRows = append(a.waitRows, r)
	}
	a.modelHead = newRow("Models in use", "", false)
	a.modelHead.set("", false)
	for i := 0; i < maxRows; i++ {
		r := newRow("", "", false)
		r.set("", false)
		a.modelRows = append(a.modelRows, r)
	}
	systray.AddSeparator()
	onClick(systray.AddMenuItem("Show Radar", ""), a.show)
	a.stay = systray.AddMenuItemCheckbox("Stay in Foreground", "Keep the radar window above the other windows", regBool(stayValue))
	onClick(a.stay, func() {
		setRegBool(stayValue, !a.stay.Checked())
		a.applyStay()
	})
	onClick(systray.AddMenuItem("Open in Browser", ""), func() { _ = shellOpen(a.url) })
	a.login = systray.AddMenuItemCheckbox("Start at Login", "Start Copilot Radar with the tray icon when you log in", startsAtLogin())
	onClick(a.login, func() {
		setStartAtLogin(!startsAtLogin())
		if startsAtLogin() {
			a.login.Check()
		} else {
			a.login.Uncheck()
		}
	})
	systray.AddSeparator()
	systray.AddMenuItem(appName+" "+a.version, "").Disable()
	onClick(systray.AddMenuItem("Quit Copilot Radar", ""), systray.Quit)
}

func (a *winApp) update(state []byte) {
	st := statusOf(state)
	active := st.working + st.waiting
	line := "No active agents"
	if active > 0 {
		line = fmt.Sprintf("%d working · %d waiting for you", st.working, st.waiting)
	}
	a.status.set(line, true)
	systray.SetTooltip(appName + " — " + line)

	waits := make([]string, len(st.waits))
	for i, w := range st.waits {
		waits[i] = w.title
	}
	models := make([]string, len(st.models))
	for i, m := range st.models {
		models[i] = m.String()
	}
	a.mu.Lock()
	a.waits = st.waits
	a.mu.Unlock()
	setRows(a.waitHead, a.waitRows, "Waiting for you", waits)
	setRows(a.modelHead, a.modelRows, "Models in use", models)
	a.setIcon(st.radar(), active)
}

// iconKey tells which icon the tray shows. iconSize is an icon of the radar window.
type iconKey struct {
	status radarStatus
	blips  int
	ink    rgba
	set    bool
}

type iconSize struct {
	iconKey
	size int
}

func (a *winApp) setIcon(st radarStatus, active int) {
	ink := inkOf()
	key := iconKey{st, min(active, len(iconBlips)), ink, true}
	a.mu.Lock()
	changed := key != a.icon
	a.icon = key
	a.mu.Unlock()
	if changed {
		systray.SetIcon(iconICO(st, key.blips, ink))
		a.applyWindowIcon()
	}
}

// applyWindowIcon shows the status icon also on the taskbar button of the radar window.
func (a *winApp) applyWindowIcon() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.hwnd == 0 || !a.icon.set {
		return
	}
	if a.icons == nil {
		a.icons = map[iconSize]uintptr{}
	}
	for which, metric := range map[uintptr]uintptr{0: smCxSmIcon, 1: smCxIcon} {
		size := int(call(pGetSystemMetrics, metric))
		if size <= 0 {
			size = 16 << which
		}
		k := iconSize{a.icon, size}
		h, ok := a.icons[k]
		if !ok {
			p := iconPNG(a.icon.status, a.icon.blips, size, a.icon.ink)
			h = call(pCreateIconFromResourceEx, uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)), 1, 0x00030000, uintptr(size), uintptr(size), 0)
			a.icons[k] = h
		}
		if h != 0 {
			call(pPostMessage, a.hwnd, wmSetIcon, which, h)
		}
	}
}

func (a *winApp) applyStay() {
	on := regBool(stayValue)
	if on {
		a.stay.Check()
	} else {
		a.stay.Uncheck()
	}
	a.mu.Lock()
	h := a.hwnd
	a.mu.Unlock()
	if h == 0 {
		return
	}
	after := ^uintptr(1) // HWND_NOTOPMOST
	if on {
		after = ^uintptr(0) // HWND_TOPMOST
	}
	call(pSetWindowPos, h, after, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActive)
}

func (a *winApp) show() {
	a.mu.Lock()
	h, opening := a.hwnd, a.opening
	if h == 0 && !opening {
		a.opening = true
	}
	a.mu.Unlock()
	switch {
	case h != 0:
		activate(h)
	case !opening:
		go a.runWindow()
	}
}

// runWindow shows the radar in a WebView2 window. The window has its own thread with a message loop.
// When the user closes the window, the thread stops. The next "Show Radar" makes a new window.
func (a *winApp) runWindow() {
	runtime.LockOSThread() // Do not unlock: the thread ends with the goroutine.
	call(pCoInitializeEx, 0, 2)
	dpi := int(call(pGetDpiForSystem))
	if dpi <= 0 {
		dpi = 96
	}
	w, h := 1440*dpi/96, 900*dpi/96
	if sw := int(call(pGetSystemMetrics, smCxScreen)); sw > 0 {
		w = min(w, sw*9/10)
	}
	if sh := int(call(pGetSystemMetrics, smCyScreen)); sh > 0 {
		h = min(h, sh*9/10)
	}
	wv := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  filepath.Join(dataDir(), "WebView2"),
		WindowOptions: webview2.WindowOptions{
			Title:  appName,
			Width:  uint(w),
			Height: uint(h),
			Center: true,
		},
	})
	if wv == nil {
		log.Printf("WebView2 is not available. The radar opens in the browser.")
		// go-webview2 shows its window before it embeds WebView2. Remove the empty window.
		if w := call(pFindWindow, utf16("webview"), utf16(appName)); w != 0 {
			call(pDestroyWindow, w)
		}
		a.mu.Lock()
		a.opening = false
		a.mu.Unlock()
		_ = shellOpen(a.url)
		return
	}
	hwnd := uintptr(wv.Window())
	one := int32(1)
	call(pDwmSetWindowAttribute, hwnd, 20, uintptr(unsafe.Pointer(&one)), 4) // DWMWA_USE_IMMERSIVE_DARK_MODE
	a.mu.Lock()
	a.hwnd, a.opening = hwnd, false
	a.mu.Unlock()
	a.applyWindowIcon()
	a.applyStay()
	wv.Navigate(a.url)
	activate(hwnd)
	wv.Run()
	a.mu.Lock()
	a.hwnd = 0
	a.mu.Unlock()
}

// jump shows the app of the session in row i of "Waiting for you".
func (a *winApp) jump(i int) {
	a.mu.Lock()
	var t jumpTarget
	ok := i < len(a.waits) && (i < maxRows-1 || len(a.waits) <= maxRows)
	if ok {
		t = a.waits[i]
	}
	a.mu.Unlock()
	if !ok {
		a.show()
		return
	}
	if t.url != "" && shellOpen(t.url) == nil {
		return
	}
	if exe := editorExe(t.editor); exe != "" {
		args := []string{}
		if t.folder != "" {
			args = append(args, t.folder)
		}
		if cmd := exec.Command(exe, args...); cmd.Start() == nil {
			go func() { _ = cmd.Wait() }()
			return
		}
	}
	if h := windowOf(t.pid); h != 0 {
		activate(h)
		return
	}
	a.show()
}

func (a *winApp) closeWindow() {
	a.mu.Lock()
	h := a.hwnd
	a.mu.Unlock()
	if h != 0 {
		call(pPostMessage, h, wmClose, 0, 0)
	}
}

// Run shows the tray icon and the radar window. It returns only when the app quits.
func Run(ctx context.Context, opt Options, h Source) {
	mutex, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(`Local\CopilotRadarApp`))
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		// Another Copilot Radar app runs. Show its window, or tell the user where its icon is.
		if w := call(pFindWindow, utf16("webview"), utf16(appName)); w != 0 {
			activate(w)
		} else {
			call(pMessageBox, 0, utf16("Copilot Radar is already running. Use its icon in the notification area of the taskbar."), utf16(appName), mbIconInfo)
		}
		return
	}
	if mutex != 0 {
		defer windows.CloseHandle(mutex)
	}
	call(pSetProcessDpiAwarenessContext, ^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2
	_ = os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", "FF0D1217")

	a := &winApp{url: opt.URL, version: opt.Version, showAtStart: opt.ShowWindow}
	systray.SetOnTapped(a.show)
	go func() {
		<-ctx.Done()
		systray.Quit()
	}()
	systray.Run(func() {
		a.buildMenu()
		go func() {
			ch := h.Subscribe()
			defer h.Unsubscribe(ch)
			if b := h.State(); b != nil {
				a.update(b)
			} else {
				a.setIcon(statusIdle, 0)
			}
			for {
				select {
				case <-ctx.Done():
					return
				case b := <-ch:
					a.update(b)
				}
			}
		}()
		if a.showAtStart {
			a.show()
		}
	}, a.closeWindow)
}
