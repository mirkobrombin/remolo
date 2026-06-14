package main

import (
	"fmt"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
	"github.com/mirkobrombin/remolo/internal/config"
)

// AliasCmd manages saved host aliases so a short name can be used instead of a
// full token (remolo's answer to ~/.ssh/config).
type AliasCmd struct {
	Add AliasAddCmd `cmd:"" help:"Save a token under an alias name"`
	Rm  AliasRmCmd  `cmd:"" help:"Remove a saved alias"`
	Ls  AliasLsCmd  `cmd:"" help:"List saved aliases"`

	cli.Base
}

// AliasAddCmd saves an alias.
type AliasAddCmd struct {
	Name       string `arg:"" required:"true" help:"Alias name"`
	Token      string `arg:"" required:"true" help:"The session token to save"`
	Relay      string `cli:"relay" help:"Preferred relay server for this alias"`
	Rendezvous string `cli:"rendezvous" help:"Preferred rendezvous broker for this alias"`
	Note       string `cli:"note" help:"Free-form note"`

	cli.Base
}

func (c *AliasAddCmd) Run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.SetAlias(c.Name, config.Alias{Token: c.Token, Relay: c.Relay, Rendezvous: c.Rendezvous, Note: c.Note})
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("remolo: saved alias %q (%s)\n", c.Name, config.Path())
	return nil
}

// AliasRmCmd removes an alias.
type AliasRmCmd struct {
	Name string `arg:"" required:"true" help:"Alias name to remove"`

	cli.Base
}

func (c *AliasRmCmd) Run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.RemoveAlias(c.Name) {
		return fmt.Errorf("no alias named %q", c.Name)
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("remolo: removed alias %q\n", c.Name)
	return nil
}

// AliasLsCmd lists aliases.
type AliasLsCmd struct {
	cli.Base
}

func (c *AliasLsCmd) Run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	names := cfg.AliasNames()
	if len(names) == 0 {
		fmt.Println("No saved aliases. Add one with: remolo alias add <name> <token>")
		return nil
	}
	for _, n := range names {
		a, _ := cfg.GetAlias(n)
		extra := ""
		if a.Relay != "" {
			extra += " relay=" + a.Relay
		}
		if a.Rendezvous != "" {
			extra += " rendezvous=" + a.Rendezvous
		}
		if a.Note != "" {
			extra += " (" + a.Note + ")"
		}
		fmt.Printf("%-16s %s...%s\n", n, firstN(a.Token, 20), extra)
	}
	return nil
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
