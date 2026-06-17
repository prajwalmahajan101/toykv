# vhs script — render the toykv-tui demo for README.
# Regenerate:  go install github.com/charmbracelet/vhs@latest && vhs assets/tui.tape
#
# Produces assets/tui.gif. Drop a single frame to assets/tui.png for the
# static fallback. Adjust Set Width/Height to taste; the README references
# the gif at its native resolution.

Output assets/tui.gif

Set FontSize 14
Set Width 1200
Set Height 700
Set Theme "Catppuccin Mocha"

# Pre-seed a server on :6390 with a few keys.
Hide
Type "./bin/toykv -addr :6390 -dir ./data -appendfsync no &"
Enter
Sleep 600ms
Type "./bin/toykv-cli -addr :6390 SET hello world && ./bin/toykv-cli -addr :6390 SET counter 42 EX 30 && ./bin/toykv-cli -addr :6390 SET note 'first release' EX 120"
Enter
Sleep 400ms
Type "clear"
Enter
Show

# Launch the TUI.
Type "./bin/toykv-tui -addr :6390 -refresh 2s"
Enter
Sleep 2s

# Navigate, filter, raw command.
Type "j"
Sleep 600ms
Type "j"
Sleep 600ms
Type "/"
Sleep 300ms
Type "co"
Sleep 1s
Escape
Sleep 400ms
Type ":"
Sleep 300ms
Type "INCR counter"
Enter
Sleep 1.2s

Type "q"
Sleep 600ms
