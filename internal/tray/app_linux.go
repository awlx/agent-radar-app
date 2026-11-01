package tray

import (
	"context"
	_ "embed"
	"log"
	"os"
	"os/exec"
)

const Supported = true

//go:embed window_linux.js
var linuxWindow []byte

func Run(ctx context.Context, opts Options, _ Source) {
	if err := runLinuxWindow(ctx, opts); err != nil && ctx.Err() == nil {
		log.Fatalf("native Linux window (requires GJS, GTK 4 and WebKitGTK 6): %v", err)
	}
}

func runLinuxWindow(ctx context.Context, opts Options) error {
	file, err := os.CreateTemp("", "agent-radar-window-*.js")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(linuxWindow); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	args := []string{"-m", file.Name(), opts.URL}
	if !opts.ShowWindow {
		args = append(args, "--background")
	}
	cmd := exec.CommandContext(ctx, "gjs", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

var linuxEditors = map[string]string{
	"Code": "code", "Code - Insiders": "code-insiders",
	"Cursor": "cursor", "VSCodium": "codium",
}

func linuxTargetCommand(t jumpTarget) (string, []string) {
	if t.url != "" {
		return "xdg-open", []string{t.url}
	}
	if editor := linuxEditors[t.editor]; editor != "" && t.folder != "" {
		return editor, []string{"--reuse-window", "--", t.folder}
	}
	// Wayland has no portable API to focus a terminal by PID or an unsaved editor window.
	return "", nil
}

func showTarget(t jumpTarget) bool {
	name, args := linuxTargetCommand(t)
	if name == "" {
		log.Printf("show app: exact terminal or unsaved-window focus is not available on Linux")
		return false
	}
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		log.Printf("show app: %v", err)
		return false
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("show app: launcher failed: %v", err)
		}
	}()
	return true
}
