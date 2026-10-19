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
	"strings"
	"syscall"
	"time"

	"github.com/awlx/copilot-radar/internal/collector"
	"github.com/awlx/copilot-radar/internal/demo"
	"github.com/awlx/copilot-radar/internal/server"
	"github.com/awlx/copilot-radar/internal/tray"
)

var version = "dev"

// appBuild is "true" in the Windows app build ("Copilot Radar.exe"). The app build starts in app mode.
var appBuild = ""

func main() {
	listen := flag.String("listen", "127.0.0.1:47900", "address to listen on. Use 0.0.0.0:47900 to expose on the LAN")
	token := flag.String("token", os.Getenv("COPILOT_RADAR_TOKEN"), "access token for non-loopback listeners (default: random, printed at start)")
	noAuth := flag.Bool("no-auth", false, "disable the token check on non-loopback listeners (not recommended)")
	poll := flag.Duration("poll", 2*time.Second, "poll interval")
	snapshot := flag.Bool("snapshot", false, "print a JSON summary of the agents and exit")
	all := flag.Bool("all", false, "with -snapshot: include idle sessions")
	showVersion := flag.Bool("version", false, "print the version and exit")
	appFlag := flag.Bool("app", false, "show the radar in a window with a menu bar icon (macOS) or a tray icon (Windows)")
	background := flag.Bool("background", false, "with -app: start with the menu bar or tray icon only, without the window")
	demoFlag := flag.Bool("demo", false, "show mock agents instead of the agents on this computer (for screenshots)")
	os.Args = append(os.Args[:1], withoutPSN(os.Args[1:])...)
	flag.Parse()
	appMode := *appFlag || inAppBundle() || appBuild == "true"
	if appMode && !tray.Supported {
		log.Fatal("-app needs Windows, or macOS and a build with cgo")
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
	if loopback {
		*token = ""
	} else if !*noAuth && *token == "" {
		*token = server.RandomToken()
	}

	hub := server.NewHub(col, *poll)
	srv := &http.Server{
		Addr:              *listen,
		Handler:           server.NewHandler(hub, *token, loopback),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil && appMode && !listenSet {
		// Another copilot-radar can use the default port. The app then uses a free port.
		ln, err = net.Listen("tcp", net.JoinHostPort(host, "0"))
	}
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	_, port, _ = net.SplitHostPort(ln.Addr().String())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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

// localURL is the URL for the app window. The window always connects through the loopback address.
func localURL(port, token string) string {
	u := "http://127.0.0.1:" + port + "/"
	if token != "" {
		u += "?token=" + url.QueryEscape(token)
	}
	return u
}

// inAppBundle reports whether the binary runs from Copilot Radar.app.
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
		log.Printf("copilot-radar %s: http://%s:%s%s", version, h, port, q)
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
