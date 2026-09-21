#!/usr/bin/env python3
"""Drive the real dirdiff binary through a pty and print what it painted.

There is no automated test for the Bubble Tea layer (raw terminal I/O),
so this is how key handling and rendering get exercised end to end — it
is what caught the multi-rune key bug (SPEC.md §9), where a fast 'l' then
'c' arrived as one tea.KeyMsg.

    go build -o /tmp/dirdiff ./cmd/dirdiff
    scripts/pty-smoke.py /tmp/dirdiff testdata/playground/left testdata/playground/right

Bubble Tea blocks on start until the terminal answers its capability
queries, so this answers them (background color, cursor position, device
attributes); without that the program never paints a single byte.

Pass a key script as KEYS=down,right,c,left,left — names come from the
table below, anything else is sent as literal text.
"""

import fcntl
import os
import pty
import re
import select
import struct
import sys
import termios
import time

KEYS = {
    "up": b"\x1b[A",
    "down": b"\x1b[B",
    "right": b"\x1b[C",
    "left": b"\x1b[D",
    "enter": b"\r",
    "esc": b"\x1b",
    "space": b" ",
    "backspace": b"\x7f",
    "pgup": b"\x1b[5~",
    "pgdown": b"\x1b[6~",
    "home": b"\x1b[H",
    "end": b"\x1b[F",
}

ROWS, COLS = 40, 140

# Everything the terminal emits that isn't text: CSI/OSC sequences, and
# the charset/keypad two-byte escapes.
ANSI = re.compile(r"\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-9;?]*[a-zA-Z@]|\x1b[()][B0]|\x1b[=>]")


def respond(fd, data):
    """Answer the queries Bubble Tea waits for before its first paint."""
    if b"\x1b]11;?" in data:  # background color
        os.write(fd, b"\x1b]11;rgb:0000/0000/0000\x1b\\")
    if b"\x1b[6n" in data:  # cursor position report
        os.write(fd, b"\x1b[1;1R")
    if b"\x1b[c" in data or b"\x1b[>c" in data or b"\x1b[>0c" in data:
        os.write(fd, b"\x1b[?1;2c")
    if b"\x1b[?u" in data:  # kitty keyboard protocol
        os.write(fd, b"\x1b[?0u")


def drain(fd, seconds):
    out = b""
    end = time.time() + seconds
    while time.time() < end:
        ready, _, _ = select.select([fd], [], [], 0.1)
        if not ready:
            continue
        try:
            chunk = os.read(fd, 1 << 20)
        except OSError:  # child exited and closed its end
            break
        if not chunk:
            break
        out += chunk
        respond(fd, chunk)
    return out


def render(raw):
    text = ANSI.sub("", raw.decode("utf-8", "replace"))
    return [line.rstrip() for line in re.split(r"\r\n|\n|\r", text) if line.strip()]


def main():
    if len(sys.argv) < 2:
        sys.exit(__doc__)
    argv = sys.argv[1:]
    keys = [k for k in os.environ.get("KEYS", "down,right,c,left,left").split(",") if k]

    pid, fd = pty.fork()
    if pid == 0:
        os.environ["TERM"] = "xterm-256color"
        os.execv(argv[0], argv)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))

    frames = [("start", drain(fd, 3.0))]
    for key in keys:
        os.write(fd, KEYS.get(key, key.encode()))
        frames.append((key, drain(fd, 1.2)))
    os.write(fd, b"q")
    time.sleep(0.3)
    try:
        os.close(fd)
    except OSError:
        pass

    for label, raw in frames:
        print(f"===== {label} ({len(raw)} bytes) =====")
        # Only the tail of a frame matters: a frame is every repaint since
        # the last key, and the last one is what's on screen.
        print("\n".join(render(raw)[-(ROWS + 4):]))


if __name__ == "__main__":
    main()
