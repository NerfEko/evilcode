Output "demo/demo-search.gif"

Set Shell "bash"
Set FontFamily "Agave Nerd Font Mono"
Set FontSize 15
Set Width 1360
Set Height 760
Set Theme "Catppuccin Frappe"
Set Padding 16
Set TypingSpeed 45ms
Set WindowBar Colorful
Set BorderRadius 8

Hide
Type "export HOME=/tmp/evildemo-home3 XDG_CONFIG_HOME=$HOME/.config XDG_DATA_HOME=$HOME/.local/share XDG_CACHE_HOME=$HOME/.cache XDG_STATE_HOME=$HOME/.local/state"
Enter
Type "rm -rf $HOME; mkdir -p $HOME"
Enter
Type "clear; EVILCODE_CONFIG=/tmp/evildemo-config.toml EVILCODE_DETERMINISTIC=1 EVILCODE_PROVIDER=mock EVILCODE_SCENARIO=demo-search EVILCODE_MOCK_STREAM_DELAY=35ms EVILCODE_DEMO_NO_LOGO=1 /home/eko/projects/evilcode/evilcode"
Enter
Sleep 1000ms
Show

# turn 1: a real captured transcript, real tool call args, replayed here —
# the tool *results* are canned too (internal/tools.Canned), so this needs
# no external repo checked out. ~14s of real streaming at this pace.
Type "search the compositor code in src for any .unwrap() calls that could panic and tell me if any are outside test code"
Sleep 500ms
Enter
Sleep 17000ms

# turn 2: a short, real follow-up
Type "good to know, thanks"
Sleep 500ms
Enter
Sleep 3000ms

# turn 3: the model's own offer, taken up — a real, much larger audit
# (six tool rounds, ~45s of streaming at this pace)
Type "yeah, widen it to expect() and indexing too"
Sleep 500ms
Enter
Sleep 50000ms

Sleep 3s
