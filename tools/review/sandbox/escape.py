"""Escape what a pull request's code printed before it reaches a terminal.

Reads bytes on stdin, writes text on stdout (#988, item 4). The output of
somebody else's tests is untrusted: an ESC sequence can move the cursor, set the
window title, or rewrite the lines above, and a bidirectional override can make
one line read as another.

Everything that is not printable is written as \\xNN, \\uNNNN or \\UNNNNNNNN:
the C0 controls except newline and tab (so a carriage return cannot overwrite a
line), DEL, the C1 controls (U+0080 to U+009F: U+009B is a one-character CSI),
the format characters (bidirectional overrides, zero-width), the line and
paragraph separators, and any byte that is not valid UTF-8. A backslash is left
as it is: the escape is for a reader, not a parser.

tools/review/sandbox/sandbox_test.go (TestTheEscaperLeavesNoControlCharacter)
fails when any of those classes is let through.
"""

import sys
import unicodedata

KEEP = "\n\t"
ESCAPED_CATEGORIES = ("Cc", "Cf", "Zl", "Zp", "Cs", "Co")


def escape(data: bytes) -> str:
    text = data.decode("utf-8", errors="surrogateescape")
    out = []
    for ch in text:
        if ch in KEEP:
            out.append(ch)
            continue
        cp = ord(ch)
        if 0xDC80 <= cp <= 0xDCFF:  # an invalid byte, kept by surrogateescape
            out.append("\\x%02x" % (cp - 0xDC00))
        elif unicodedata.category(ch) in ESCAPED_CATEGORIES:
            if cp < 0x100:
                out.append(f"\\x{cp:02x}")
            elif cp < 0x10000:
                out.append(f"\\u{cp:04x}")
            else:
                out.append(f"\\U{cp:08x}")
        else:
            out.append(ch)
    return "".join(out)


def main() -> int:
    data = sys.stdin.buffer.read()
    sys.stdout.buffer.write(escape(data).encode("utf-8"))
    sys.stdout.buffer.flush()
    return 0


if __name__ == "__main__":
    sys.exit(main())
