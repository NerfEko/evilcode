package testdata

// big is a probe fixture for the side panel: a file long enough that its diff
// does not fit the panel, which is where a panel that cannot scroll shows the
// first half and silently drops the rest.
//
// Committed in the state BEFORE the scenario's write, for the same reason
// clamp.go is: the probe restores testdata from git before every run, so an
// uncommitted fixture stays rewritten and every later run sees no diff at all.
func alpha(a int) int    { return a + 1 }
func bravo(a int) int    { return a + 2 }
func charlie(a int) int  { return a + 3 }
func delta(a int) int    { return a + 4 }
func echo(a int) int     { return a + 5 }
func foxtrot(a int) int  { return a + 6 }
func golf(a int) int     { return a + 7 }
func hotel(a int) int    { return a + 8 }
func india(a int) int    { return a + 9 }
func juliet(a int) int   { return a + 10 }
func kilo(a int) int     { return a + 11 }
func lima(a int) int     { return a + 12 }
func mike(a int) int     { return a + 13 }
func november(a int) int { return a + 14 }
func oscar(a int) int    { return a + 15 }
