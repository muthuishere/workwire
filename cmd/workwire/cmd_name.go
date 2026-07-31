package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/muthuishere/workwire/internal/config"
	"github.com/muthuishere/workwire/internal/origin"
)

// cmdName prints the peer name a join would use for a directory, and nothing
// else — so a skill, a script and `listen` itself can never disagree about who
// this session is. Same precedence as listen: skill.json agentName, else
// `<repo>-<branch>`.
func cmdName(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("name", flag.ExitOnError)
	dir := fs.String("dir", "", "working tree to derive the name from (default: cwd)")
	fs.Parse(args)

	d := *dir
	if d == "" {
		d, _ = os.Getwd()
	}
	name := loadSkillConfig(skillConfigPath(cfg)).AgentName
	if name == "" {
		name = origin.DeriveName(d)
	}
	if name == "" {
		return fmt.Errorf("could not derive an agent name for %s", absOf(d))
	}
	fmt.Println(name)
	return nil
}
