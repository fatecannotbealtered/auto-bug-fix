package main

import "github.com/fatecannotbealtered/auto-bug-fix/cmd"

func main() {
	cmd.SetChangelog(ChangelogMarkdown)
	cmd.Execute()
}
