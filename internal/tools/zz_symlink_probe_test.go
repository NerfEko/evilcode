package tools

import "testing"

func TestZZSymlinkProbe(t *testing.T) {
	set := LoadSkills([]string{"../../.evilcode/skills"})
	for _, s := range set.Index() {
		desc := s.Desc
		if len(desc) > 60 {
			desc = desc[:60] + "…"
		}
		body, err := set.Body(s.Name)
		t.Logf("%-30s bodylen=%-6d %v  %s", s.Name, len(body), err, desc)
	}
	t.Logf("TOTAL %d", len(set.Index()))
}
