#!/usr/bin/env python3
"""Check panel-sized symbolic artwork with GTK's foreground and status palettes."""
from pathlib import Path

import gi

gi.require_version("Gtk", "3.0")
gi.require_version("Gdk", "3.0")
from gi.repository import Gdk, Gio, Gtk

ROOT = Path(__file__).resolve().parent / "extension/icons"


def rgba(value):
    color = Gdk.RGBA()
    if not color.parse(value):
        raise ValueError(value)
    return color


for size in (16, 24, 32):
    for foreground in ("#222222", "#eeeeee", "#000000", "#ffffff"):
        images = []
        for state in ("working", "waiting", "idle", "offline"):
            icon = Gio.FileIcon.new(Gio.File.new_for_path(str(ROOT / f"radar-{state}-symbolic.svg")))
            info = Gtk.IconTheme.get_default().lookup_by_gicon(icon, size, Gtk.IconLookupFlags.FORCE_SYMBOLIC)
            pixbuf, symbolic = info.load_symbolic(
                rgba(foreground), rgba("#32c759"), rgba("#ffcc00"), rgba("#ff3b30"),
            )
            pixels = bytes(pixbuf.get_pixels())
            if not symbolic or pixbuf.get_n_channels() != 4 or not any(pixels[3::4]):
                raise RuntimeError(f"{state} is not visible symbolic artwork")
            if not any(pixels[i + 3] > 128 for i in range(0, len(pixels) - 3, 4)):
                raise RuntimeError(f"{state} has no solid panel-scale strokes")
            images.append(pixels)
        if len(set(images)) != 4:
            raise RuntimeError(f"Status badges are indistinguishable at {size} pixels")
print("PASS: four distinct symbolic badges at 16/24/32 pixels in light, dark and high-contrast palettes")
