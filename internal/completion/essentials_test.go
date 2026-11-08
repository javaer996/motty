package completion

import (
	"strings"
	"testing"
)

func candidateNames(result Result) []string {
	out := make([]string, len(result.Candidates))
	for i, candidate := range result.Candidates {
		out[i] = candidate.Text
	}
	return out
}

func hasCandidateText(result Result, text string) bool {
	for _, candidate := range result.Candidates {
		if candidate.Text == text {
			return true
		}
	}
	return false
}

// The imported snapshot has no netstat, ss or ip, which is exactly the gap
// Essentials exists to fill.
func TestEssentialsCoverMissingNetworkCommands(t *testing.T) {
	catalog := Essentials()
	if catalog == nil {
		t.Fatal("the essentials catalog did not build")
	}

	result := Complete(catalog, "nets")
	if !hasCandidateText(result, "netstat") {
		t.Fatalf("netstat is not offered: %v", candidateNames(result))
	}
	result = Complete(catalog, "s")
	if !hasCandidateText(result, "ss") {
		t.Fatalf("ss is not offered: %v", candidateNames(result))
	}
	result = Complete(catalog, "ip ")
	for _, want := range []string{"address", "link", "route", "neigh"} {
		if !hasCandidateText(result, want) {
			t.Fatalf("ip %s is not offered: %v", want, candidateNames(result))
		}
	}
	result = Complete(catalog, "ip -")
	for _, want := range []string{"-4", "-6", "-s", "-j"} {
		if !hasCandidateText(result, want) {
			t.Fatalf("ip %s is not offered: %v", want, candidateNames(result))
		}
	}
}

func TestEssentialsOfferCommonOptions(t *testing.T) {
	catalog := Essentials()

	result := Complete(catalog, "netstat -")
	for _, want := range []string{"-t", "-u", "-l", "-n", "-p", "-a"} {
		if !hasCandidateText(result, want) {
			t.Fatalf("netstat %s is not offered: %v", want, candidateNames(result))
		}
	}
	result = Complete(catalog, "netstat --")
	if !hasCandidateText(result, "--tcp") || !hasCandidateText(result, "--numeric") {
		t.Fatalf("netstat long options are missing: %v", candidateNames(result))
	}

	// A cluster that has already been typed must not make the rest of the line
	// unsupported; -tuln is how ss is actually used.
	result = Complete(catalog, "ss -tulnp ")
	if result.Suppressed {
		t.Fatal("a short-option cluster suppressed the line")
	}
	if !hasCandidateText(result, "--all") {
		t.Fatalf("the line after a cluster has no options: %v", candidateNames(result))
	}

	// A multi-character option name is one option, not a cluster.
	result = Complete(catalog, "nmap -sS ")
	if result.Suppressed {
		t.Fatal("a multi-character option suppressed the line")
	}
	if !hasCandidateText(result, "--open") {
		t.Fatalf("the line after -sS has no options: %v", candidateNames(result))
	}
}

// The table is ordered by how often an option is used, so the first chips are
// the useful ones rather than whatever sorts first alphabetically.
func TestEssentialsOrderCommonOptionsFirst(t *testing.T) {
	result := Complete(Essentials(), "netstat -")
	want := []string{"-a", "-t", "-u", "-l", "-n", "-p", "-r", "-i"}
	if len(result.Candidates) < len(want) {
		t.Fatalf("only %v", candidateNames(result))
	}
	for index, text := range want {
		if result.Candidates[index].Text != text {
			t.Fatalf("netstat chip %d = %q, want %q (%v)", index, result.Candidates[index].Text, text, candidateNames(result))
		}
	}

	result = Complete(Essentials(), "ip -")
	want = []string{"-4", "-6", "-s", "-br", "-j", "-p", "-c", "-o"}
	for index, text := range want {
		if result.Candidates[index].Text != text {
			t.Fatalf("ip chip %d = %q, want %q (%v)", index, result.Candidates[index].Text, text, candidateNames(result))
		}
	}
}

func TestEssentialsCoverShellBuiltins(t *testing.T) {
	catalog := Essentials()
	for _, line := range []string{"ech", "print", "expor", "sourc"} {
		result := Complete(catalog, line)
		if len(result.Candidates) == 0 {
			t.Fatalf("%q offered nothing", line)
		}
	}
	result := Complete(catalog, "read -")
	for _, want := range []string{"-r", "-p", "-t", "-s"} {
		if !hasCandidateText(result, want) {
			t.Fatalf("read %s is not offered: %v", want, candidateNames(result))
		}
	}
}

func TestEssentialsOptionsAreWellFormed(t *testing.T) {
	catalog := Essentials()
	var checkNode func(*Node)
	checkNode = func(node *Node) {
		for _, flag := range node.Flags {
			if flag.Long == "" && flag.Short == "" {
				t.Fatalf("%s has an option with no name", strings.Join(node.Path(), " "))
			}
			if strings.HasPrefix(flag.Long, "-") || strings.HasPrefix(flag.Short, "-") {
				t.Fatalf("%s has an option that already carries a dash", strings.Join(node.Path(), " "))
			}
		}
		for _, child := range node.Children {
			checkNode(child)
		}
	}
	for _, root := range catalog.Roots() {
		checkNode(root)
	}
}
