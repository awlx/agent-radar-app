package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/awlx/agent-radar-app/internal/collector"
	"github.com/awlx/agent-radar-app/internal/demo"
	"github.com/awlx/agent-radar-app/internal/server"
	"github.com/awlx/agent-radar-app/internal/tray"
)

var version = "dev"

// appBuild is "true" in the Windows app build ("Agent Radar.exe"). The app build starts in app mode.
var appBuild = ""

func main() {
	listen := flag.String("listen", "127.0.0.1:47900", "address to listen on. Use 0.0.0.0:47900 to expose on the LAN")
	token := flag.String("token", envToken(), "access token for non-loopback listeners (default: random, printed at start)")
	noAuth := flag.Bool("no-auth", false, "disable the token check on non-loopback listeners (not recommended)")
	poll := flag.Duration("poll", 2*time.Second, "poll interval")
	snapshot := flag.Bool("snapshot", false, "print a JSON summary of the agents and exit")
	all := flag.Bool("all", false, "with -snapshot: include idle sessions")
	showVersion := flag.Bool("version", false, "print the version and exit")
	appFlag := flag.Bool("app", false, "show the radar in a native window (Linux requires GJS, GTK 4 and WebKitGTK 6)")
	gnome := flag.Bool("gnome", false, "run the loopback backend for the Linux GNOME extension and desktop launcher")
	parentPID := flag.Int("parent-pid", 0, "internal: stop the Linux backend if its owning app exits")
	background := flag.Bool("background", false, "with -app: start with the menu bar or tray icon only, without the window")
	demoFlag := flag.Bool("demo", false, "show mock agents instead of the agents on this computer (for screenshots)")
	os.Args = append(os.Args[:1], withoutPSN(os.Args[1:])...)
	flag.Parse()
	appMode := *appFlag || inAppBundle() || appBuild == "true"
	if appMode && !tray.Supported {
		log.Fatal("-app needs Linux with GJS/GTK/WebKit, Windows, or macOS with cgo")
	}
	listenSet := false
	flag.Visit(func(f *flag.Flag) { listenSet = listenSet || f.Name == "listen" })

	if *showVersion {
		fmt.Println(version)
		return
	}
	var col server.Source = collector.New()
	if *demoFlag {
		col = demo.New()
	}
	if *snapshot {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(collector.Summarize(col.Snapshot(), !*all))
		return
	}

	host, port, err := net.SplitHostPort(*listen)
	if err != nil {
		log.Fatalf("invalid -listen %q: %v", *listen, err)
	}
	loopback := server.IsLoopbackHost(host)
	if *gnome && (runtime.GOOS != "linux" || !loopback) {
		log.Fatal("-gnome requires Linux and a loopback listener")
	}
	if *parentPID < 0 || (*parentPID > 0 && (!*gnome || os.Getppid() != *parentPID)) {
		log.Fatal("-parent-pid requires the owning Linux app as the actual parent of a -gnome backend")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *parentPID > 0 {
		go func() {
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if os.Getppid() != *parentPID {
						log.Print("owning Radar app exited; stopping its backend")
						stop()
						return
					}
				}
			}
		}()
	}
	if appMode && runtime.GOOS == "linux" {
		n, err := strconv.Atoi(port)
		if host != "127.0.0.1" || err != nil || n < 1 || n > 65535 {
			log.Fatal("the Linux app requires a fixed 127.0.0.1 port")
		}
		exe, err := os.Executable()
		if err != nil {
			log.Fatal(err)
		}
		tray.Run(ctx, tray.Options{URL: localURL(port, ""), Version: version, ShowWindow: !*background,
			Backend: exe, Demo: *demoFlag, Poll: *poll}, nil)
		return
	}
	if loopback {
		*token = ""
	} else if !*noAuth && *token == "" {
		*token = server.RandomToken()
	}

	hub := server.NewHub(col, *poll)
	// Only the app on this computer can show the app of a session. A LAN client must not move windows here.
	var jump func([]byte, string) bool
	if (appMode || *gnome) && loopback && !*demoFlag {
		jump = tray.Jump
	}
	srv := &http.Server{
		Addr:              *listen,
		Handler:           server.NewHandler(hub, *token, loopback, jump),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil && appMode && !listenSet && runtime.GOOS != "linux" {
		// Another agent-radar can use the default port. The app then uses a free port.
		ln, err = net.Listen("tcp", net.JoinHostPort(host, "0"))
	}
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	_, port, _ = net.SplitHostPort(ln.Addr().String())

	go hub.Run(ctx)

	printURLs(host, port, *token, loopback)
	if !loopback && *noAuth {
		log.Printf("WARNING: no token check. Each host on the network can see your agent commands and file paths.")
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if appMode {
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatal(err)
			}
		}()
		tray.Run(ctx, tray.Options{URL: localURL(port, *token), Version: version, ShowWindow: !*background}, hub)
		return
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// envToken reads AGENT_RADAR_TOKEN, or COPILOT_RADAR_TOKEN from before the rename.
func envToken() string {
	if t := os.Getenv("AGENT_RADAR_TOKEN"); t != "" {
		return t
	}
	return os.Getenv("COPILOT_RADAR_TOKEN")
}

// localURL is the URL for the app window. The window always connects through the loopback address.
func localURL(port, token string) string {
	u := "http://127.0.0.1:" + port + "/"
	if token != "" {
		u += "?token=" + url.QueryEscape(token)
	}
	return u
}

// inAppBundle reports whether the binary runs from Agent Radar.app.
func inAppBundle() bool {
	exe, err := os.Executable()
	return err == nil && strings.Contains(exe, ".app/Contents/MacOS/")
}

// withoutPSN removes the -psn_ argument that old macOS versions give to apps that Finder starts.
func withoutPSN(args []string) []string {
	out := args[:0:0]
	for _, a := range args {
		if !strings.HasPrefix(a, "-psn_") {
			out = append(out, a)
		}
	}
	return out
}

func printURLs(host, port, token string, loopback bool) {
	q := ""
	if token != "" {
		q = "/?token=" + token
	}
	hosts := []string{host}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		hosts = lanIPs()
	}
	for _, h := range hosts {
		if strings.Contains(h, ":") {
			h = "[" + h + "]"
		}
		log.Printf("agent-radar %s: http://%s:%s%s", version, h, port, q)
	}
	if !loopback && token != "" {
		log.Printf("Open the URL with the token one time. The browser then keeps a cookie.")
	}
}

func lanIPs() []string {
	var out []string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLinkLocalUnicast() {
			if v4 := n.IP.To4(); v4 != nil {
				out = append(out, v4.String())
			}
		}
	}
	if len(out) == 0 {
		out = []string{"127.0.0.1"}
	}
	return out
}
