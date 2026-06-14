package main

import (
	"fmt"
	"strings"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
	"github.com/mirkobrombin/remolo/internal/config"
)

// GroupCmd manages host groups for fleet operations (remolo exec @group ...).
type GroupCmd struct {
	Set GroupSetCmd `cmd:"" help:"Define a group as a set of alias names"`
	Ls  GroupLsCmd  `cmd:"" help:"List groups"`
	Rm  GroupRmCmd  `cmd:"" help:"Remove a group"`

	cli.Base
}

// GroupSetCmd defines a group.
type GroupSetCmd struct {
	Name    string   `arg:"" required:"true" help:"Group name"`
	Members []string `arg:"" required:"true" help:"Alias names in the group"`

	cli.Base
}

func (c *GroupSetCmd) Run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Groups == nil {
		cfg.Groups = map[string][]string{}
	}
	cfg.Groups[c.Name] = c.Members
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("remolo: group %q = %s\n", c.Name, strings.Join(c.Members, ", "))
	return nil
}

// GroupLsCmd lists groups.
type GroupLsCmd struct {
	cli.Base
}

func (c *GroupLsCmd) Run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.Groups) == 0 {
		fmt.Println("No groups. Define one with: remolo group set <name> <alias>...")
		return nil
	}
	for name, members := range cfg.Groups {
		fmt.Printf("%-16s %s\n", name, strings.Join(members, ", "))
	}
	return nil
}

// GroupRmCmd removes a group.
type GroupRmCmd struct {
	Name string `arg:"" required:"true" help:"Group name"`

	cli.Base
}

func (c *GroupRmCmd) Run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, ok := cfg.Groups[c.Name]; !ok {
		return fmt.Errorf("no group named %q", c.Name)
	}
	delete(cfg.Groups, c.Name)
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("remolo: removed group %q\n", c.Name)
	return nil
}
