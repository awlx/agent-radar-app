{
  lib,
  buildGoModule,
  wrapGAppsHook4,
  copyDesktopItems,
  makeDesktopItem,
  gjs,
  gtk4,
  webkitgtk_6_0,
  libsoup_3,
  glib,
  gobject-introspection,
  glib-networking,
  version ? "0.1.4",
}:

# Agent Radar without GNOME Shell: the Go server with its embedded web UI, and
# the native window (`agent-radar -app`), which is a GJS script on GTK 4 +
# WebKitGTK 6. Only the top-bar extension needs GNOME Shell; it is left out.
buildGoModule {
  pname = "agent-radar";
  inherit version;

  # Only what Go and the tests need, so the flake and nix-build see the same
  # source (and a README edit does not rebuild anything).
  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod ../go.sum ../cmd ../internal ../linux ../macos ../windows
    ];
  };
  vendorHash = "sha256-ONovBTO6abi23A4pgm1reVdTaYN6uAY5mU9luG81Wbw=";

  subPackages = [ "cmd/agent-radar" ];
  env.CGO_ENABLED = 0;
  ldflags = [ "-s" "-w" "-X main.version=${version}" ];

  # The window runs `gjs` from PATH and imports Gtk 4, Soup 3 and WebKit 6
  # through GObject introspection. wrapGAppsHook4 collects their typelibs (and
  # the transitive ones: Graphene, Pango, …) so it works from any desktop.
  nativeBuildInputs = [ wrapGAppsHook4 gobject-introspection copyDesktopItems ];
  buildInputs = [ gtk4 webkitgtk_6_0 libsoup_3 glib glib-networking ];
  preFixup = ''
    gappsWrapperArgs+=(--prefix PATH : ${lib.makeBinPath [ gjs ]})
  '';

  postInstall = ''
    install -Dm644 linux/extension/icons/radar-idle-symbolic.svg \
      $out/share/icons/hicolor/scalable/apps/agent-radar.svg
  '';

  desktopItems = [
    (makeDesktopItem {
      name = "agent-radar";
      desktopName = "Agent Radar";
      comment = "Live map of local Copilot sessions and a remote Hermes Agent";
      exec = "agent-radar -app";
      icon = "agent-radar";
      categories = [ "Development" "Monitor" ];
    })
  ];

  meta = {
    description = "Live map of local GitHub Copilot agents and a remote Hermes Agent (server, web UI and GTK window, no GNOME Shell needed)";
    homepage = "https://github.com/awlx/agent-radar-app";
    license = lib.licenses.mit;
    mainProgram = "agent-radar";
    platforms = lib.platforms.linux;
  };
}
