Output "demo/diff.gif"

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
Type "export HOME=/tmp/evildemo-home XDG_CONFIG_HOME=$HOME/.config XDG_DATA_HOME=$HOME/.local/share XDG_CACHE_HOME=$HOME/.cache XDG_STATE_HOME=$HOME/.local/state"
Enter
Type "rm -rf $HOME; mkdir -p $HOME"
Enter
Type "clear; EVILCODE_CONFIG=/tmp/evildemo-config.toml EVILCODE_DETERMINISTIC=1 EVILCODE_PROVIDER=mock EVILCODE_SCENARIO=diff EVILCODE_MOCK_STREAM_DELAY=35ms EVILCODE_DEMO_NO_LOGO=1 /home/eko/projects/evilcode/evilcode"
Enter
Sleep 1000ms
Show

# turn 1: an edit, real inline diff, then cycle the side panel
Type "fix the off-by-one in the clamp bounds check"
Sleep 500ms
Enter
Sleep 2500ms
Type "/diff"
Enter
Sleep 1300ms
Enter
Sleep 1300ms
Escape
Sleep 500ms

# turn 2: a natural follow-up, still the same session
Type "looks right, thanks"
Sleep 500ms
Enter
Sleep 2000ms

Sleep 2500ms

Hide
Ctrl+C
Sleep 200ms
Ctrl+C
Sleep 300ms
Type "git -C /home/eko/projects/evilcode checkout -- testdata/"
Enter
Sleep 500ms
