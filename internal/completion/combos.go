package completion

import "strings"

// KindCombo marks a suggestion that completes a whole command line with a
// common parameter combination, rather than a single token from the catalog.
const KindCombo Kind = 3

// combo is one curated parameter combination. Line is the whole command as it
// would be typed; the description is English here and translated by
// LocalizeDesc.
type combo struct {
	line string
	desc string
}

// maxCombos bounds how many combinations one answer may carry. The inline bar
// shows a handful; the rest stay out of the way.
const maxCombos = 6

// combos are the combinations people type most often, most useful first. They
// are deliberately literal: every one is safe to type into a POSIX shell.
var combos = []combo{
	// git
	{"git status -sb", "Short status with the branch"},
	{"git log --oneline --graph --decorate -20", "Compact commit graph"},
	{"git diff --stat", "Changed files with line counts"},
	{"git diff --cached", "What is staged"},
	{"git add -A", "Stage every change"},
	{"git add -p", "Stage changes hunk by hunk"},
	{"git commit -am \"\"", "Stage tracked changes and commit"},
	{"git commit --amend --no-edit", "Add to the last commit, keep its message"},
	{"git push -u origin HEAD", "Push and track the current branch"},
	{"git pull --rebase", "Pull, rebasing local commits"},
	{"git fetch --all --prune", "Fetch every remote and drop dead branches"},
	{"git switch -c ", "Create and switch to a branch"},
	{"git switch -", "Switch back to the previous branch"},
	{"git branch -vv", "Branches with their upstream"},
	{"git stash push -u -m \"\"", "Stash everything, untracked files included"},
	{"git stash pop", "Restore the last stash"},
	{"git restore --staged ", "Unstage a file"},
	{"git reset --soft HEAD~1", "Undo the last commit, keep its changes"},
	{"git rebase -i HEAD~", "Rewrite the last commits"},
	{"git show --stat", "The last commit with changed files"},
	{"git remote -v", "Remote repositories"},
	{"git tag -l", "List tags"},
	{"git blame -L ", "Who changed these lines"},

	// docker
	{"docker ps -a", "Every container, running or not"},
	{"docker ps", "Running containers"},
	{"docker images", "Local images"},
	{"docker logs -f --tail 100 ", "Follow a container's last 100 lines"},
	{"docker exec -it ", "Open a shell in a running container"},
	{"docker run --rm -it ", "Run an interactive container, remove it after"},
	{"docker compose up -d", "Start the stack in the background"},
	{"docker compose down", "Stop and remove the stack"},
	{"docker compose logs -f", "Follow the stack's logs"},
	{"docker compose ps", "The stack's containers"},
	{"docker system prune -af", "Delete every unused image, network and cache"},

	// kubectl
	{"kubectl get pods -o wide", "Pods with node and address"},
	{"kubectl get pods -A -o wide", "Pods in every namespace"},
	{"kubectl get svc -A", "Services in every namespace"},
	{"kubectl get nodes -o wide", "Nodes with address and version"},
	{"kubectl logs -f ", "Follow a pod's log"},
	{"kubectl describe pod ", "Everything about a pod"},
	{"kubectl apply -f ", "Apply a manifest"},
	{"kubectl delete pod ", "Delete a pod"},
	{"kubectl exec -it  -- sh", "Run a shell in a pod"},
	{"kubectl config get-contexts", "Kubeconfig contexts"},
	{"kubectl config use-context ", "Switch context"},
	{"kubectl rollout restart deploy/", "Restart a deployment"},
	{"kubectl port-forward svc/ 8080:80", "Forward a service port locally"},

	// files
	{"ls -lah", "Long listing with sizes and hidden files"},
	{"ls -lat", "Newest files first"},
	{"cp -r ", "Copy a directory"},
	{"mv -v ", "Move and say what moved"},
	{"rm -rf ", "Delete a directory and its contents"},
	{"mkdir -p ", "Create parents as needed"},
	{"chmod +x ", "Make a file executable"},
	{"chown -R ", "Change ownership recursively"},
	{"du -sh *", "Size of every entry here"},
	{"df -h", "Free space per filesystem"},
	{"free -h", "Memory in use"},

	// search and transfer
	{"grep -rn \"\" .", "Search this tree with line numbers"},
	{"grep -ri \"\" .", "Search this tree, ignoring case"},
	{"find . -name \"\"", "Find by name below this directory"},
	{"find . -type f -name \"\"", "Find files by name"},
	{"tar -xzvf ", "Extract a .tar.gz"},
	{"tar -czvf ", "Create a .tar.gz"},
	{"curl -fsSL ", "Download quietly, failing on HTTP errors"},
	{"curl -I ", "Show response headers"},
	{"wget -c ", "Resume an interrupted download"},
	{"rsync -avz ", "Sync over SSH, compressed"},

	// ssh
	{"ssh -p ", "Connect on another port"},
	{"ssh -i ", "Connect with a private key"},
	{"ssh -L :localhost: ", "Forward a remote port locally"},
	{"scp -r ", "Copy a directory over SSH"},

	// services
	{"systemctl status ", "A unit's state"},
	{"systemctl restart ", "Restart a unit"},
	{"journalctl -u  -f", "Follow one unit's journal"},
	{"journalctl -xe", "Recent errors from the journal"},
	{"tail -f ", "Follow a growing file"},

	// node and go
	{"npm install", "Install dependencies"},
	{"npm run dev", "Run the development script"},
	{"npm run build", "Build for production"},
	{"npm test", "Run the test script"},
	{"go test ./...", "Test every package"},
	{"go run .", "Run this package"},
	{"go build ./...", "Build every package"},
	{"go mod tidy", "Add missing and drop unused modules"},
	{"go vet ./...", "Vet every package"},
	{"python3 -m venv .venv", "Create a virtual environment"},
	{"pip install -r requirements.txt", "Install the pinned requirements"},
}

// Combos answers the curated parameter combinations whose line continues what
// has been typed. The insert is the rest of the line after line, so nothing the
// user typed is rewritten.
func Combos(line string, limit int) []Candidate {
	if line == "" || len(line) > maxLineBytes {
		return nil
	}
	if limit <= 0 || limit > maxCombos {
		limit = maxCombos
	}
	out := make([]Candidate, 0, limit)
	for _, entry := range combos {
		if len(out) >= limit {
			break
		}
		if !strings.HasPrefix(entry.line, line) || entry.line == line {
			continue
		}
		rest := entry.line[len(line):]
		if strings.TrimSpace(rest) == "" {
			continue
		}
		out = append(out, Candidate{
			Text:   entry.line,
			Insert: rest,
			Desc:   entry.desc,
			Kind:   KindCombo,
		})
	}
	return out
}
