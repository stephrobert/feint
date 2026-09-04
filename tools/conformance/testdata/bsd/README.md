BSD-behaving shims for the tools that made the Day-2 library's tests go red on
macOS-15 (CI run 33893151746), so Linux can see what macOS sees. Each mimics the
one difference that mattered and delegates the rest to /usr/bin:

- wc: pads a bare count to width 8, as BSD wc does.
- paste: refuses to run with no file operand (BSD usage error); `-` is stdin.
- sed: a bare `-i` takes the NEXT argument as the backup suffix.

TestTheDay2LibraryHoldsUnderBSDTools puts this directory first in PATH.
